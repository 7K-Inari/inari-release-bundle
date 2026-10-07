#!/usr/bin/env bash
# e2e kubectl tunnel / gateway mode (plan §7.2): the FULL real chain —
# inari-kubeproxy (built from the sibling inari-server checkout) →
# inari-tunnel-agent (built from the sibling inari-agent checkout) → a real
# kube-apiserver — plus Keycloak, Postgres, and OpenFGA as plain docker
# containers (no kind), same style as kubectl-access.sh.
#
# Asserts the live contract:
#   - happy path: a viewer's kubectl request transits the tunnel and is
#     authorized by the real API server via hub-minted Impersonate-*
#     (viewer reads succeed, editor-only ops are denied — the agent SA holds
#     ONLY impersonate rights, so success proves impersonation);
#   - upgrade path: no tunnel agent connected → 503 with remediation text,
#     fast (no hang);
#   - global kill-switch: INARI_KUBECTL_ACCESS_ENABLED=false → 410 for
#     users, tunnel streams rejected;
#   - per-cluster revoke: disabling the tunnel-<id> Keycloak client denies
#     tunnel admission (agent can no longer mint a token) → users get 503;
#   - max-lifetime reaper: a tiny INARI_KUBEPROXY_MAX_TUNNEL_LIFETIME cuts a
#     long-lived watch with the max_lifetime close reason.
#
# Prereqs: docker, kubectl, jq, curl, openssl, and sibling checkouts of
# inari-server and inari-agent. Env: NETWORK, KEEP, INARI_SERVER_DIR,
# INARI_AGENT_DIR, KC_IMAGE, APISERVER_IMAGE, ETCD_IMAGE, PG_IMAGE,
# OPENFGA_IMAGE.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
INARI_SERVER_DIR="${INARI_SERVER_DIR:-$SCRIPT_DIR/../../../../inari-server}"
INARI_AGENT_DIR="${INARI_AGENT_DIR:-$SCRIPT_DIR/../../../../inari-agent}"
[ -d "$INARI_SERVER_DIR/cmd/inari-kubeproxy" ] || { echo "inari-server checkout not found at $INARI_SERVER_DIR (set INARI_SERVER_DIR)" >&2; exit 1; }
[ -d "$INARI_AGENT_DIR/cmd/inari-tunnel-agent" ] || { echo "inari-agent checkout not found at $INARI_AGENT_DIR (set INARI_AGENT_DIR)" >&2; exit 1; }

NETWORK="${NETWORK:-inari-tunnel-e2e}"
KC_IMAGE="${KC_IMAGE:-quay.io/keycloak/keycloak:26.0}"
APISERVER_IMAGE="${APISERVER_IMAGE:-registry.k8s.io/kube-apiserver:v1.34.0}"
ETCD_IMAGE="${ETCD_IMAGE:-gcr.io/etcd-development/etcd:v3.5.21}"
PG_IMAGE="${PG_IMAGE:-postgres:16-alpine}"
OPENFGA_IMAGE="${OPENFGA_IMAGE:-openfga/openfga:latest}"
NGINX_IMAGE="${NGINX_IMAGE:-nginx:1.27-alpine}"
TOOLS_IMAGE=curlimages/curl:8.10.1
KUBEPROXY_IMAGE=inari/kubeproxy:e2e
AGENT_IMAGE=inari/agent:e2e
KEEP="${KEEP:-false}"

ETCD_NAME="$NETWORK-etcd"
API_NAME="$NETWORK-apiserver"
KC_NAME="$NETWORK-keycloak"
PG_NAME="$NETWORK-postgres"
FGA_NAME="$NETWORK-openfga"
KP_NAME="$NETWORK-kubeproxy"
SHIM_NAME="$NETWORK-tlsshim"
TA_NAME="$NETWORK-tunnel-agent"
TOOLS_NAME="$NETWORK-tools"

KC_HOST="keycloak:8443"
ISSUER="https://${KC_HOST}/realms/inari"
REALM=inari
ORG=acme
CLIENT_ID="org-${ORG}-kubectl"
GROUP_PATH="/tenant-${ORG}/viewers"
VIEWER_ROLE="tenant-${ORG}-viewer"
USERNAME=dev-viewer
PASSWORD=dev-viewer
CLUSTER_ID=clu-e2e1
TUNNEL_CLIENT="tunnel-${CLUSTER_ID}"
DB_ORG_ID="org:e2e-${ORG}"     # organizations.id; FGA object strips the org: prefix
# kubectl hits the TLS shim (production topology: TLS terminates at the LB in
# front of kubeproxy — kubectl refuses bearer tokens over cleartext HTTP);
# curl assertions hit kubeproxy directly.
PROXY_URL="https://kubeproxy-tls:8443/api/v1/tenants/${ORG}/clusters/${CLUSTER_ID}/proxy"
PROXY_URL_HTTP="http://kubeproxy:8090/api/v1/tenants/${ORG}/clusters/${CLUSTER_ID}/proxy"
WORKDIR_E2E="$(mktemp -d /tmp/inari-tunnel-e2e.XXXXXX)"

log() { printf '\033[1;34m[tunnel-e2e]\033[0m %s\n' "$*"; }
die() { printf '\033[1;31m[tunnel-e2e] %s\033[0m\n' "$*" >&2; exit 1; }
need() { command -v "$1" >/dev/null || die "missing prerequisite: $1"; }
need docker; need kubectl; need jq; need curl; need openssl

dump_logs() {
  for c in "$KP_NAME" "$TA_NAME" "$API_NAME" "$KC_NAME"; do
    printf '\033[1;33m--- logs: %s ---\033[0m\n' "$c" >&2
    docker logs "$c" 2>&1 | tail -30 >&2 || true
  done
}
cleanup() {
  if ! $KEEP; then
    docker rm -f "$TA_NAME" "$KP_NAME" "$SHIM_NAME" "$TOOLS_NAME" "$API_NAME" "$KC_NAME" \
      "$FGA_NAME" "$PG_NAME" "$ETCD_NAME" >/dev/null 2>&1 || true
    docker network rm "$NETWORK" >/dev/null 2>&1 || true
    rm -rf "$WORKDIR_E2E" >/dev/null 2>&1 || true
  fi
}
trap 'rc=$?; [ $rc -ne 0 ] && dump_logs; cleanup; exit $rc' EXIT

# xcurl runs curl inside the tools container (in-network DNS + CA trust).
xcurl() { docker exec "$TOOLS_NAME" curl -sf -m 30 --cacert /tmp/ca.crt "$@"; }

# user_token mints a fresh user access token (aud kubernetes, groups, org).
user_token() {
  xcurl "$ISSUER/protocol/openid-connect/token" \
    -d grant_type=password -d client_id="$CLIENT_ID" \
    -d username="$USERNAME" -d password="$PASSWORD" | jq -r .access_token
}
# kctl runs kubectl from the tools container against the kubeproxy gateway
# with a fresh user token (CA-pinned TLS via the shim).
kctl() { docker exec "$TOOLS_NAME" /tmp/kubectl --server "$PROXY_URL" --certificate-authority /tmp/ca.crt --token "$(user_token)" "$@"; }

# --- 0. e2e PKI -------------------------------------------------------------
log "generating e2e CA and certificates"
openssl req -x509 -newkey rsa:2048 -nodes -days 1 \
  -keyout "$WORKDIR_E2E/ca.key" -out "$WORKDIR_E2E/ca.crt" \
  -subj "/CN=inari-tunnel-e2e-ca" >/dev/null 2>&1
mkcert() { # name cn san
  openssl req -newkey rsa:2048 -nodes -keyout "$WORKDIR_E2E/$1.key" \
    -out "$WORKDIR_E2E/$1.csr" -subj "/CN=$2" >/dev/null 2>&1
  printf 'subjectAltName=%s' "$3" > "$WORKDIR_E2E/$1.ext"
  openssl x509 -req -days 1 -in "$WORKDIR_E2E/$1.csr" \
    -CA "$WORKDIR_E2E/ca.crt" -CAkey "$WORKDIR_E2E/ca.key" -CAcreateserial \
    -out "$WORKDIR_E2E/$1.crt" -extfile "$WORKDIR_E2E/$1.ext" >/dev/null 2>&1
}
mkcert keycloak keycloak "DNS:keycloak"
mkcert apiserver kube-apiserver "DNS:kube-apiserver,DNS:localhost,IP:127.0.0.1"
mkcert kubeproxy-tls kubeproxy-tls "DNS:kubeproxy-tls"
openssl req -newkey rsa:2048 -nodes -keyout "$WORKDIR_E2E/admin.key" \
  -out "$WORKDIR_E2E/admin.csr" -subj "/CN=admin/O=system:masters" >/dev/null 2>&1
openssl x509 -req -days 1 -in "$WORKDIR_E2E/admin.csr" \
  -CA "$WORKDIR_E2E/ca.crt" -CAkey "$WORKDIR_E2E/ca.key" -CAcreateserial \
  -out "$WORKDIR_E2E/admin.crt" >/dev/null 2>&1
openssl genrsa -out "$WORKDIR_E2E/sa.key" 2048 >/dev/null 2>&1
openssl rsa -in "$WORKDIR_E2E/sa.key" -pubout -out "$WORKDIR_E2E/sa.pub" >/dev/null 2>&1

log "writing AuthenticationConfiguration (issuer $ISSUER)"
{
cat <<EOF
apiVersion: apiserver.config.k8s.io/v1
kind: AuthenticationConfiguration
jwt:
- issuer:
    url: ${ISSUER}
    certificateAuthority: |
EOF
sed 's/^/      /' "$WORKDIR_E2E/ca.crt"
cat <<EOF
    audiences:
    - kubernetes
    - ${CLIENT_ID}
    audienceMatchPolicy: MatchAny
  claimMappings:
    username:
      claim: preferred_username
      prefix: "keycloak:"
    groups:
      claim: groups
      prefix: ""
  claimValidationRules:
  - expression: 'has(claims.organization) && "${ORG}" in claims.organization'
    message: "token organization does not match this tenant"
EOF
} > "$WORKDIR_E2E/authn.yaml"

# --- 1. build component images from sibling checkouts -----------------------
log "building $KUBEPROXY_IMAGE from $INARI_SERVER_DIR"
docker build -q --target kubeproxy -t "$KUBEPROXY_IMAGE" \
  -f "$INARI_SERVER_DIR/deploy/docker/Dockerfile" "$INARI_SERVER_DIR" >/dev/null
log "building $AGENT_IMAGE from $INARI_AGENT_DIR"
docker build -q -t "$AGENT_IMAGE" "$INARI_AGENT_DIR" >/dev/null

# --- 2. containers -----------------------------------------------------------
SUBNET=172.31.78.0/24
ETCD_IP=172.31.78.2
API_IP=172.31.78.3
KC_IP=172.31.78.4
TOOLS_IP=172.31.78.5
PG_IP=172.31.78.6
FGA_IP=172.31.78.7
KP_IP=172.31.78.8
TA_IP=172.31.78.9
SHIM_IP=172.31.78.10

log "pulling images"
for img in "$ETCD_IMAGE" "$APISERVER_IMAGE" "$KC_IMAGE" "$PG_IMAGE" "$OPENFGA_IMAGE" "$NGINX_IMAGE" "$TOOLS_IMAGE"; do
  docker pull -q "$img" >/dev/null
done
docker network create --subnet "$SUBNET" "$NETWORK" >/dev/null 2>&1 || true

log "starting etcd"
docker rm -f "$ETCD_NAME" >/dev/null 2>&1 || true
docker run -d --name "$ETCD_NAME" --network "$NETWORK" --ip "$ETCD_IP" --network-alias etcd \
  "$ETCD_IMAGE" /usr/local/bin/etcd \
  --advertise-client-urls http://etcd:2379 \
  --listen-client-urls http://0.0.0.0:2379 >/dev/null

log "starting postgres"
docker rm -f "$PG_NAME" >/dev/null 2>&1 || true
docker run -d --name "$PG_NAME" --network "$NETWORK" --ip "$PG_IP" --network-alias postgres \
  -e POSTGRES_USER=inari -e POSTGRES_PASSWORD=inari -e POSTGRES_DB=inari \
  "$PG_IMAGE" >/dev/null

log "starting OpenFGA (in-memory datastore)"
docker rm -f "$FGA_NAME" >/dev/null 2>&1 || true
docker run -d --name "$FGA_NAME" --network "$NETWORK" --ip "$FGA_IP" --network-alias openfga \
  "$OPENFGA_IMAGE" run >/dev/null

log "starting Keycloak ($KC_IMAGE)"
docker rm -f "$KC_NAME" >/dev/null 2>&1 || true
docker create --name "$KC_NAME" --network "$NETWORK" --ip "$KC_IP" --network-alias keycloak \
  -e KC_BOOTSTRAP_ADMIN_USERNAME=admin -e KC_BOOTSTRAP_ADMIN_PASSWORD=admin \
  -e KC_HOSTNAME="https://keycloak:8443" \
  -e KC_HTTPS_CERTIFICATE_FILE=/kc-tls.crt \
  -e KC_HTTPS_CERTIFICATE_KEY_FILE=/kc-tls.key \
  -e KC_HEALTH_ENABLED=true \
  "$KC_IMAGE" start-dev >/dev/null
# openssl writes keys 0600 owned by the host uid; the container's keycloak
# user (uid 1000) must be able to read them regardless of the runner uid.
chmod 644 "$WORKDIR_E2E/keycloak.crt" "$WORKDIR_E2E/keycloak.key"
docker cp "$WORKDIR_E2E/keycloak.crt" "$KC_NAME:/kc-tls.crt"
docker cp "$WORKDIR_E2E/keycloak.key" "$KC_NAME:/kc-tls.key"
docker start "$KC_NAME" >/dev/null

log "starting tools container"
docker rm -f "$TOOLS_NAME" >/dev/null 2>&1 || true
docker run -d --name "$TOOLS_NAME" --network "$NETWORK" --ip "$TOOLS_IP" --user 0 \
  --add-host "keycloak:$KC_IP" --add-host "kube-apiserver:$API_IP" \
  --add-host "kubeproxy:$KP_IP" --add-host "kubeproxy-tls:$SHIM_IP" \
  --add-host "openfga:$FGA_IP" --add-host "postgres:$PG_IP" \
  "$TOOLS_IMAGE" sleep 3600 >/dev/null
docker cp "$WORKDIR_E2E/ca.crt" "$TOOLS_NAME:/tmp/ca.crt"
docker cp "$(command -v kubectl)" "$TOOLS_NAME:/tmp/kubectl"
docker exec "$TOOLS_NAME" chmod +x /tmp/kubectl

log "waiting for Keycloak https"
for i in $(seq 1 90); do
  xcurl "https://keycloak:8443/realms/master/.well-known/openid-configuration" >/dev/null 2>&1 && break
  sleep 4
  [ "$i" = 90 ] && die "keycloak never became ready"
done

log "waiting for postgres"
for i in $(seq 1 30); do
  docker exec "$PG_NAME" pg_isready -U inari -d inari >/dev/null 2>&1 && break
  sleep 2
  [ "$i" = 30 ] && die "postgres never became ready"
done

log "waiting for OpenFGA"
for i in $(seq 1 30); do
  docker exec "$TOOLS_NAME" curl -sf -m 5 http://openfga:8080/healthz >/dev/null 2>&1 && break
  sleep 2
  [ "$i" = 30 ] && die "openfga never became healthy"
done

# --- 3. database migrations + tenant seed ------------------------------------
# kubeproxy does not self-migrate (the inari-server binary owns that). Apply
# the embedded goose migrations offline: each file's Up section (everything
# before `-- +goose Down`; `-- +goose` comment markers stripped) is plain
# SQL — the only StatementBegin blocks are single plpgsql functions, which
# psql parses natively — so concatenating the Up sections in file order and
# piping them through psql reproduces `goose up` for this tree. Verified
# against goose's own output by the script author's runs; if inari-server
# ever ships a non-trivial StatementBegin, revisit this transform.
log "applying migrations from inari-server checkout (psql, goose-annotation-aware)"
MIG_SQL="$WORKDIR_E2E/migrations.sql"
: > "$MIG_SQL"
for f in "$INARI_SERVER_DIR"/internal/db/migrations/*.sql; do
  sed '/^-- +goose Down/,$d; /^-- +goose/d' "$f" >> "$MIG_SQL"
done
docker cp "$MIG_SQL" "$PG_NAME:/tmp/migrations.sql"
docker exec "$PG_NAME" psql -U inari -d inari -v ON_ERROR_STOP=1 -q -f /tmp/migrations.sql
docker exec "$PG_NAME" psql -U inari -d inari -v ON_ERROR_STOP=1 -q \
  -c "INSERT INTO organizations (id, slug, display_name, keycloak_org_id)
      VALUES ('$DB_ORG_ID', '$ORG', 'Acme', 'kc-org-e2e')
      ON CONFLICT (id) DO NOTHING" \
  -c "INSERT INTO clusters (id, org_id, name, state)
      VALUES ('$CLUSTER_ID', '$DB_ORG_ID', 'e2e-cluster', 'active')
      ON CONFLICT (id) DO NOTHING"

# --- 4. Keycloak realm provisioning ------------------------------------------
# The admin-cli token expires after 60s (master realm default) but the
# assertion cases below run for minutes — mint a fresh token per kc call.
admin_token() {
  xcurl "https://keycloak:8443/realms/master/protocol/openid-connect/token" \
    -d grant_type=password -d client_id=admin-cli -d username=admin -d password=admin | jq -r .access_token
}
ADMIN_TOKEN=$(admin_token)
[ -n "$ADMIN_TOKEN" ] && [ "$ADMIN_TOKEN" != null ] || die "keycloak admin token failed"
kc() { xcurl -H "Authorization: Bearer $(admin_token)" -H "Content-Type: application/json" "$@"; }

log "creating realm $REALM + organization $ORG + group $GROUP_PATH + user $USERNAME"
kc -X POST -d '{"realm":"'$REALM'","enabled":true,"organizationsEnabled":true}' -o /dev/null \
  "https://keycloak:8443/admin/realms" || true
kc -X POST -d '{"name":"'$ORG'","alias":"'$ORG'","enabled":true,"domains":[{"name":"'$ORG'.local","verified":true}]}' -o /dev/null \
  "https://keycloak:8443/admin/realms/$REALM/organizations" || true
kc -X POST -d '{"name":"tenant-'$ORG'"}' -o /dev/null "https://keycloak:8443/admin/realms/$REALM/groups" || true
PARENT_ID=$(kc "https://keycloak:8443/admin/realms/$REALM/groups?exact=true&search=tenant-$ORG" | jq -r '.[0].id')
kc -X POST -d '{"name":"viewers"}' -o /dev/null \
  "https://keycloak:8443/admin/realms/$REALM/groups/$PARENT_ID/children" || true
kc -X POST -d '{"username":"'$USERNAME'","enabled":true,"email":"'$USERNAME'@inari.local","emailVerified":true,"firstName":"Dev","lastName":"Viewer","requiredActions":[],
  "credentials":[{"type":"password","value":"'$PASSWORD'","temporary":false}]}' \
  -o /dev/null "https://keycloak:8443/admin/realms/$REALM/users" || true
UID_KC=$(kc "https://keycloak:8443/admin/realms/$REALM/users?username=$USERNAME" | jq -r '.[0].id')
kc -X PUT -d '{"type":"password","value":"'$PASSWORD'","temporary":false}' -o /dev/null \
  "https://keycloak:8443/admin/realms/$REALM/users/$UID_KC/reset-password"
kc -X PUT -d '{"enabled":true,"emailVerified":true,"firstName":"Dev","lastName":"Viewer","requiredActions":[]}' -o /dev/null \
  "https://keycloak:8443/admin/realms/$REALM/users/$UID_KC" || true
ORG_ID=$(kc "https://keycloak:8443/admin/realms/$REALM/organizations" | jq -r '.[0].id')
kc -X POST -d '"'"$UID_KC"'"' -o /dev/null "https://keycloak:8443/admin/realms/$REALM/organizations/$ORG_ID/members" || true
GROUP_ID=$(kc "https://keycloak:8443/admin/realms/$REALM/groups/$PARENT_ID/children?exact=true&search=viewers" | jq -r '.[0].id')
[ -n "$GROUP_ID" ] && [ "$GROUP_ID" != null ] || GROUP_ID=$(kc "https://keycloak:8443/admin/realms/$REALM/groups?search=viewers" | jq -r '.[] | select(.name=="viewers") | .id' | head -1)
kc -X PUT -o /dev/null "https://keycloak:8443/admin/realms/$REALM/users/$UID_KC/groups/$GROUP_ID" || true

log "creating user client $CLIENT_ID (ROPC on for headless e2e)"
kc -X POST -d '{
  "clientId": "'$CLIENT_ID'",
  "name": "kubectl",
  "enabled": true,
  "publicClient": true,
  "standardFlowEnabled": true,
  "directAccessGrantsEnabled": true,
  "redirectUris": ["http://localhost:8000", "http://localhost:18000"],
  "attributes": {"oauth2.device.authorization.grant.enabled": "true"},
  "defaultClientScopes": ["openid", "profile", "basic", "organization"],
  "protocolMappers": [
    {"name": "groups", "protocol": "openid-connect", "protocolMapper": "oidc-group-membership-mapper",
     "config": {"claim.name": "groups", "full.path": "true", "id.token.claim": "true",
                "access.token.claim": "true", "userinfo.token.claim": "true"}},
    {"name": "audience-kubernetes", "protocol": "openid-connect", "protocolMapper": "oidc-audience-mapper",
     "config": {"included.client.audience": "kubernetes", "id.token.claim": "false",
                "access.token.claim": "true", "userinfo.token.claim": "false"}}
  ]}' -o /dev/null "https://keycloak:8443/admin/realms/$REALM/clients" || true

# create_tunnel_client provisions tunnel-<cluster-id> with the exact shape of
# tenancy.CreateTunnelClient and refreshes TUNNEL_UUID/TUNNEL_SECRET.
create_tunnel_client() {
  kc -X POST -d '{
    "clientId": "'$TUNNEL_CLIENT'",
    "enabled": true,
    "publicClient": false,
    "standardFlowEnabled": false,
    "serviceAccountsEnabled": true,
    "directAccessGrantsEnabled": false,
    "protocolMappers": [
      {"name": "cluster_id", "protocol": "openid-connect", "protocolMapper": "oidc-hardcoded-claim-mapper",
       "config": {"claim.name": "cluster_id", "claim.value": "'$CLUSTER_ID'", "jsonType.label": "String",
                  "access.token.claim": "true", "id.token.claim": "true",
                  "userinfo.token.claim": "false", "access.tokenResponse.claim": "false"}},
      {"name": "audience-inari-kubeproxy", "protocol": "openid-connect", "protocolMapper": "oidc-audience-mapper",
       "config": {"included.client.audience": "inari-kubeproxy", "id.token.claim": "false",
                  "access.token.claim": "true", "userinfo.token.claim": "false"}}
    ]}' -o /dev/null "https://keycloak:8443/admin/realms/$REALM/clients" || true
  TUNNEL_UUID=$(kc "https://keycloak:8443/admin/realms/$REALM/clients?clientId=$TUNNEL_CLIENT" | jq -r '.[0].id')
  TUNNEL_SECRET=$(kc "https://keycloak:8443/admin/realms/$REALM/clients/$TUNNEL_UUID/client-secret" | jq -r .value)
  [ -n "$TUNNEL_SECRET" ] && [ "$TUNNEL_SECRET" != null ] || die "no tunnel client secret"
}

log "creating tunnel client $TUNNEL_CLIENT (shape = tenancy.CreateTunnelClient)"
create_tunnel_client

# --- 5. API server + cluster-side RBAC ---------------------------------------
log "starting kube-apiserver ($APISERVER_IMAGE, structured JWT authn)"
docker rm -f "$API_NAME" >/dev/null 2>&1 || true
docker create --name "$API_NAME" --network "$NETWORK" --ip "$API_IP" --network-alias kube-apiserver \
  --add-host "keycloak:$KC_IP" --add-host "etcd:$ETCD_IP" \
  "$APISERVER_IMAGE" \
  kube-apiserver \
  --etcd-servers=http://etcd:2379 \
  --authentication-config=/etc/k8s/authn.yaml \
  --authorization-mode=RBAC \
  --client-ca-file=/etc/k8s/ca.crt \
  --tls-cert-file=/etc/k8s/apiserver.crt \
  --tls-private-key-file=/etc/k8s/apiserver.key \
  --service-account-signing-key-file=/etc/k8s/sa.key \
  --service-account-key-file=/etc/k8s/sa.pub \
  --service-account-issuer=https://kubernetes.default.svc \
  --bind-address=0.0.0.0 \
  --allow-privileged=true >/dev/null
docker cp "$WORKDIR_E2E/." "$API_NAME:/etc/k8s/"
docker start "$API_NAME" >/dev/null
for i in $(seq 1 90); do
  docker exec "$TOOLS_NAME" curl -sk -m 5 https://kube-apiserver:6443/healthz 2>/dev/null | grep -q ok && break
  sleep 2
  [ "$i" = 90 ] && die "kube-apiserver never became healthy"
done

cat > "$WORKDIR_E2E/admin.kubeconfig" <<EOF
apiVersion: v1
kind: Config
clusters:
- name: e2e
  cluster:
    server: https://kube-apiserver:6443
    certificate-authority: /tmp/ca.crt
users:
- name: admin
  user:
    client-certificate: /tmp/admin.crt
    client-key: /tmp/admin.key
contexts:
- name: e2e
  context: {cluster: e2e, user: admin}
current-context: e2e
EOF
docker cp "$WORKDIR_E2E/admin.kubeconfig" "$TOOLS_NAME:/tmp/admin.kubeconfig"
docker cp "$WORKDIR_E2E/admin.crt" "$TOOLS_NAME:/tmp/admin.crt"
docker cp "$WORKDIR_E2E/admin.key" "$TOOLS_NAME:/tmp/admin.key"
ADMIN_K="docker exec -i $TOOLS_NAME env KUBECONFIG=/tmp/admin.kubeconfig /tmp/kubectl"

log "applying viewer RBAC + tunnel-agent SA (impersonate-only, mirrors the agent chart)"
cat <<EOF | $ADMIN_K apply -f - >/dev/null
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: ${VIEWER_ROLE}
rules:
- apiGroups: ["*"]
  resources: ["*"]
  verbs: ["get", "list", "watch"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: tenant-${ORG}-viewers-viewer
subjects:
- kind: Group
  name: "${GROUP_PATH}"
  apiGroup: rbac.authorization.k8s.io
roleRef:
  kind: ClusterRole
  name: ${VIEWER_ROLE}
  apiGroup: rbac.authorization.k8s.io
---
apiVersion: v1
kind: ServiceAccount
metadata:
  name: inari-tunnel-agent
  namespace: default
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: inari-tunnel-agent
rules:
- apiGroups: [""]
  resources: ["users", "groups", "uids"]
  verbs: ["impersonate"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: inari-tunnel-agent
subjects:
- kind: ServiceAccount
  name: inari-tunnel-agent
  namespace: default
roleRef:
  kind: ClusterRole
  name: inari-tunnel-agent
  apiGroup: rbac.authorization.k8s.io
EOF
$ADMIN_K -n default create token inari-tunnel-agent > "$WORKDIR_E2E/sa.token"
[ -s "$WORKDIR_E2E/sa.token" ] || die "failed to mint tunnel-agent SA token"

# --- 6. kubeproxy + FGA tuples ------------------------------------------------
start_kubeproxy() { # extra env args (docker-run flags)...
  docker rm -f "$KP_NAME" >/dev/null 2>&1 || true
  docker create --name "$KP_NAME" --network "$NETWORK" --ip "$KP_IP" --network-alias kubeproxy \
    --add-host "keycloak:$KC_IP" --add-host "postgres:$PG_IP" --add-host "openfga:$FGA_IP" \
    -e SSL_CERT_FILE=/ca.crt \
    -e INARI_KUBEPROXY_LISTEN_ADDR=:8090 \
    -e INARI_DATABASE_URL="postgres://inari:inari@postgres:5432/inari?sslmode=disable" \
    -e INARI_OIDC_ISSUER_URL="$ISSUER" \
    -e INARI_OPENFGA_API_URL=http://openfga:8080 \
    -e INARI_KUBEPROXY_OPEN_RESULT_TIMEOUT=5s \
    "$@" "$KUBEPROXY_IMAGE" >/dev/null
  docker cp "$WORKDIR_E2E/ca.crt" "$KP_NAME:/ca.crt"
  docker start "$KP_NAME" >/dev/null
  for i in $(seq 1 60); do
    docker exec "$TOOLS_NAME" curl -sf -m 5 http://kubeproxy:8090/readyz >/dev/null 2>&1 && return 0
    sleep 2
  done
  docker logs "$KP_NAME" 2>&1 | tail -20 >&2
  die "kubeproxy never became ready"
}

log "starting kubeproxy"
start_kubeproxy

# TLS shim in front of kubeproxy (production LB analogue): kubectl refuses
# bearer tokens over cleartext HTTP, and the kubeproxy binary serves h2c
# only by design. One-shot: kubeproxy restarts keep the same upstream.
log "starting TLS shim for kubectl ($NGINX_IMAGE → http://kubeproxy:8090)"
cat > "$WORKDIR_E2E/shim.conf" <<'EOF'
server {
    listen 8443 ssl;
    ssl_certificate     /tls/tls.crt;
    ssl_certificate_key /tls/tls.key;
    location / {
        proxy_pass http://kubeproxy:8090;
        proxy_http_version 1.1;
        proxy_set_header Connection "";
        proxy_set_header Authorization $http_authorization;
        proxy_buffering off;
        proxy_read_timeout 3600s;
    }
}
EOF
mkdir -p "$WORKDIR_E2E/shim-tls"
cp "$WORKDIR_E2E/kubeproxy-tls.crt" "$WORKDIR_E2E/shim-tls/tls.crt"
cp "$WORKDIR_E2E/kubeproxy-tls.key" "$WORKDIR_E2E/shim-tls/tls.key"
docker rm -f "$SHIM_NAME" >/dev/null 2>&1 || true
docker create --name "$SHIM_NAME" --network "$NETWORK" --ip "$SHIM_IP" --network-alias kubeproxy-tls \
  --add-host "kubeproxy:$KP_IP" \
  "$NGINX_IMAGE" nginx -g 'daemon off;' >/dev/null
docker cp "$WORKDIR_E2E/shim.conf" "$SHIM_NAME:/etc/nginx/conf.d/default.conf"
docker cp "$WORKDIR_E2E/shim-tls/." "$SHIM_NAME:/tls/"
docker start "$SHIM_NAME" >/dev/null
for i in $(seq 1 15); do
  docker exec "$TOOLS_NAME" curl -sk -m 5 --cacert /tmp/ca.crt -o /dev/null https://kubeproxy-tls:8443/healthz && break
  sleep 2
  [ "$i" = 15 ] && { docker logs "$SHIM_NAME" 2>&1 | tail -10; die "TLS shim never came up"; }
done

log "writing OpenFGA tuples (store+model ensured by kubeproxy at boot)"
FGA_STORE=$(docker exec "$TOOLS_NAME" curl -sf http://openfga:8080/stores | jq -r '.stores[] | select(.name=="inari") | .id')
[ -n "$FGA_STORE" ] || die "openfga store 'inari' missing"
FGA_MODEL=$(docker exec "$TOOLS_NAME" curl -sf "http://openfga:8080/stores/$FGA_STORE/authorization-models" | jq -r '.authorization_models[0].id')
[ -n "$FGA_MODEL" ] && [ "$FGA_MODEL" != null ] || die "openfga model missing"
docker exec "$TOOLS_NAME" curl -sf -X POST "http://openfga:8080/stores/$FGA_STORE/write" \
  -H "Content-Type: application/json" -d '{
  "writes": {"tuple_keys": [
    {"user": "user:'"$UID_KC"'", "relation": "member", "object": "team:e2e-viewers"},
    {"user": "team:e2e-viewers#member", "relation": "clusters_kubectl", "object": "organization:e2e-'"$ORG"'"},
    {"user": "organization:e2e-'"$ORG"'", "relation": "parent", "object": "cluster:'"$CLUSTER_ID"'"}
  ]},
  "authorization_model_id": "'"$FGA_MODEL"'"
}' -o /dev/null

# --- 7. tunnel agent ------------------------------------------------------------
start_agent() { # extra env args (docker-run flags)...
  docker rm -f "$TA_NAME" >/dev/null 2>&1 || true
  docker create --name "$TA_NAME" --network "$NETWORK" --ip "$TA_IP" --network-alias tunnel-agent --user 0 \
    --add-host "keycloak:$KC_IP" --add-host "kubeproxy:$KP_IP" --add-host "kube-apiserver:$API_IP" \
    -e SSL_CERT_FILE=/ca.crt \
    -e INARI_KUBEPROXY_URL=http://kubeproxy:8090 \
    -e INARI_CLUSTER_ID="$CLUSTER_ID" \
    -e INARI_OIDC_ISSUER="$ISSUER" \
    -e INARI_CLIENT_ID="$TUNNEL_CLIENT" \
    -e INARI_CLIENT_SECRET="$TUNNEL_SECRET" \
    -e INARI_APISERVER_URL=https://kube-apiserver:6443 \
    -e INARI_SA_TOKEN_FILE=/sa.token \
    -e INARI_SA_CA_FILE=/ca.crt \
    -e INARI_TUNNEL_HEALTH_ADDR=:8082 \
    -e INARI_TUNNEL_PING_INTERVAL=5s \
    "$@" --entrypoint /inari-tunnel-agent "$AGENT_IMAGE" >/dev/null
  docker cp "$WORKDIR_E2E/ca.crt" "$TA_NAME:/ca.crt"
  docker cp "$WORKDIR_E2E/sa.token" "$TA_NAME:/sa.token"
  docker start "$TA_NAME" >/dev/null
}
wait_heartbeat() { # want: 1 = live session (fresh row), 0 = no live session (row gone or stale)
  # Freshness mirrors the control plane's tunnelAvailable semantics
  # (last_seen_at within ~the agent ping interval): a crashed kubeproxy
  # leaves a stale row behind, and only live sessions refresh it.
  for i in $(seq 1 40); do
    n=$(docker exec "$PG_NAME" psql -U inari -d inari -tAc \
      "SELECT count(*) FROM cluster_tunnel_heartbeats
       WHERE cluster_id='$CLUSTER_ID' AND last_seen_at > now() - interval '20 seconds'" 2>/dev/null || echo 0)
    [ "$n" = "$1" ] && return 0
    sleep 3
  done
  docker logs "$TA_NAME" 2>&1 | tail -15 >&2 || true
  die "live heartbeat state never became $1"
}

log "starting tunnel agent"
start_agent
wait_heartbeat 1

# --- 8. assertions --------------------------------------------------------------
log "case 1: happy path — viewer kubectl over the tunnel"
kctl get ns >/dev/null || die "kubectl get ns via gateway failed (should be allowed for viewer)"
log "  get ns OK"
CAN_I=$(kctl auth can-i create deployments -n default || true)
[ "$CAN_I" = "no" ] || die "auth can-i create deployments = $CAN_I, want no (agent SA has only impersonate — viewer identity must be in effect)"
if kctl create namespace should-fail 2>/dev/null; then
  die "kubectl create namespace succeeded for viewer (should be denied)"
fi
log "  editor-only ops denied — hub-minted Impersonate-* verified end to end"

log "case 2: upgrade path — no tunnel agent → fast 503 + remediation"
docker stop "$TA_NAME" >/dev/null
docker rm "$TA_NAME" >/dev/null
wait_heartbeat 0
BODY=$(docker exec "$TOOLS_NAME" curl -s -o /tmp/resp.txt -w '%{http_code} %{time_total}' \
  -H "Authorization: Bearer $(user_token)" "$PROXY_URL_HTTP/api/v1/namespaces")
CODE=${BODY% *}; ELAPSED=${BODY#* }
[ "$CODE" = 503 ] || die "no-tunnel request returned $CODE, want 503 (body: $(docker exec "$TOOLS_NAME" cat /tmp/resp.txt))"
docker exec "$TOOLS_NAME" grep -q "upgrade the inari-agent chart" /tmp/resp.txt \
  || die "503 body lacks remediation text: $(docker exec "$TOOLS_NAME" cat /tmp/resp.txt)"
awk "BEGIN{exit !($ELAPSED < 5.0)}" || die "503 took ${ELAPSED}s — should fail fast, not hang"
log "  503 with remediation in ${ELAPSED}s"
log "  restarting tunnel agent"
start_agent
wait_heartbeat 1

log "case 3: global kill-switch — INARI_KUBECTL_ACCESS_ENABLED=false → 410 + tunnel rejected"
start_kubeproxy -e INARI_KUBECTL_ACCESS_ENABLED=false
CODE=$(docker exec "$TOOLS_NAME" curl -s -o /tmp/resp.txt -w '%{http_code}' \
  -H "Authorization: Bearer $(user_token)" "$PROXY_URL_HTTP/api/v1/namespaces")
[ "$CODE" = 410 ] || die "kill-switch request returned $CODE, want 410 (body: $(docker exec "$TOOLS_NAME" cat /tmp/resp.txt))"
docker exec "$TOOLS_NAME" grep -q "kubectl access is disabled" /tmp/resp.txt \
  || die "410 body lacks policy text: $(docker exec "$TOOLS_NAME" cat /tmp/resp.txt)"
# Retry: the agent's reconnect attempt is on its own backoff schedule.
reject_seen=""
for i in $(seq 1 15); do
  docker logs "$TA_NAME" 2>&1 | tail -50 | grep -qi "disabled by platform policy\|unavailable" && { reject_seen=1; break; }
  sleep 2
done
[ -n "$reject_seen" ] || die "agent log shows no tunnel rejection under the kill switch"
log "  410 for users; agent stream rejected"
log "  re-enabling"
start_kubeproxy
wait_heartbeat 1

# set_tunnel_client_enabled flips the tunnel client via a full-representation
# update (KC PUT /clients/{id} replaces the whole client — a partial body
# would silently wipe serviceAccountsEnabled and the protocol mappers).
set_tunnel_client_enabled() { # true|false
  local uuid rep
  uuid=$(kc "https://keycloak:8443/admin/realms/$REALM/clients?clientId=$TUNNEL_CLIENT" | jq -r '.[0].id')
  rep=$(kc "https://keycloak:8443/admin/realms/$REALM/clients/$uuid" | jq --argjson e "$1" '.enabled = $e')
  kc -X PUT -d "$rep" -o /dev/null "https://keycloak:8443/admin/realms/$REALM/clients/$uuid"
}

log "case 4: per-cluster revoke — disable tunnel client → admission denied → 503"
set_tunnel_client_enabled false
docker restart "$TA_NAME" >/dev/null
wait_heartbeat 0
CODE=$(docker exec "$TOOLS_NAME" curl -s -o /dev/null -w '%{http_code}' \
  -H "Authorization: Bearer $(user_token)" "$PROXY_URL_HTTP/api/v1/namespaces")
[ "$CODE" = 503 ] || die "post-revoke request returned $CODE, want 503"
docker logs "$TA_NAME" 2>&1 | tail -30 | grep -qi "token\|401\|unauthor\|invalid_client" \
  || log "  (agent token failure not found in recent logs — heartbeat absence already proves no admission)"
log "  disabled client cannot mint a token; users get 503"
# Recovery mirrors the production path: re-run the (idempotent) provisioning
# — revoke the broken client, recreate, and roll the agent with the fresh
# secret (the ESO-delivered secret in production).
log "  re-provisioning tunnel client (recovery = EnsureTunnelClient re-run)"
kc -X DELETE -o /dev/null "https://keycloak:8443/admin/realms/$REALM/clients/$TUNNEL_UUID"
create_tunnel_client
start_agent
wait_heartbeat 1

# wait_proxy_ready polls the user proxy route until it stops answering 503.
# Needed after a kubeproxy restart: the heartbeat row stays fresh for ~20s
# after the old session dies, so wait_heartbeat alone can return before the
# agent has reconnected to the NEW kubeproxy — a watch fired into that gap
# 503s before the audit-open defer runs and case 5 finds no close row.
wait_proxy_ready() {
  for i in $(seq 1 30); do
    code=$(docker exec "$TOOLS_NAME" curl -s -o /dev/null -w '%{http_code}' -m 10 \
      -H "Authorization: Bearer $(user_token)" "$PROXY_URL_HTTP/api/v1/namespaces" || true)
    [ "$code" = 200 ] && return 0
    sleep 2
  done
  docker logs "$KP_NAME" 2>&1 | tail -15 >&2 || true
  die "proxy route never returned 200 after tunnel wait (last code: $code)"
}

log "case 5: max-lifetime reaper — tiny INARI_KUBEPROXY_MAX_TUNNEL_LIFETIME cuts a watch"
start_kubeproxy -e INARI_KUBEPROXY_MAX_TUNNEL_LIFETIME=3s
wait_heartbeat 1
wait_proxy_ready
set +e
WATCH_OUT=$(docker exec "$TOOLS_NAME" timeout 20 /tmp/kubectl --server "$PROXY_URL" --certificate-authority /tmp/ca.crt --token "$(user_token)" get ns -w 2>&1)
WATCH_RC=$?
set -e
# The watch must NOT still be running at the 20s timeout (rc 124 = reaper failed).
[ "$WATCH_RC" != 124 ] || die "watch survived 20s under a 3s max lifetime — reaper did not fire"
# The close reason is audited to the outbox (cluster.kubectl_proxy_close).
for i in $(seq 1 10); do
  n=$(docker exec "$PG_NAME" psql -U inari -d inari -tAc \
    "SELECT count(*) FROM outbox WHERE event_type='cluster.kubectl_proxy_close' AND payload->>'reason'='max_lifetime'" 2>/dev/null || echo 0)
  [ "$n" -ge 1 ] && break
  sleep 1
done
[ "$n" -ge 1 ] || die "no cluster.kubectl_proxy_close outbox row with reason=max_lifetime (watch output: $WATCH_OUT)"
log "  watch cut by reaper with max_lifetime close reason"

log "case 6: clean session replacement — heartbeat row survives (access-info must not flap)"
# Regression gate for the M1W7 live-run flapping bug (inari-server PR #152):
# a clean agent restart replaces the stream; kubeproxy must KEEP the
# cluster_tunnel_heartbeats row on clean disconnect / eviction so
# access-info tunnelAvailable never flaps false for a live tunnel.
docker restart "$TA_NAME" >/dev/null
flap_seen=""
for i in $(seq 1 12); do
  n=$(docker exec "$PG_NAME" psql -U inari -d inari -tAc \
    "SELECT count(*) FROM cluster_tunnel_heartbeats
     WHERE cluster_id='$CLUSTER_ID' AND last_seen_at > now() - interval '20 seconds'" 2>/dev/null || echo 0)
  if [ "$n" = "0" ]; then flap_seen=1; break; fi
  sleep 1
done
[ -z "$flap_seen" ] || die "heartbeat row disappeared during clean agent restart — access-info would flap tunnelAvailable=false (regression of inari-server#152)"
wait_heartbeat 1
CODE=$(docker exec "$TOOLS_NAME" curl -s -o /dev/null -w '%{http_code}' -m 10 \
  -H "Authorization: Bearer $(user_token)" "$PROXY_URL_HTTP/api/v1/namespaces")
[ "$CODE" = 200 ] || die "post-restart request returned $CODE, want 200 (replaced session must serve traffic)"
log "  heartbeat row never dropped; replaced session serves 200"

log "ALL ASSERTIONS PASSED — kubeproxy → tunnel-agent → apiserver gateway chain works"
