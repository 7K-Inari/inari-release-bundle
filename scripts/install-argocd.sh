#!/usr/bin/env bash
# Installs ArgoCD onto the current cluster (day-0 prerequisite for the
# gitops/ layout). Idempotent.
set -euo pipefail

ARGOCD_VERSION="${ARGOCD_VERSION:-v3.1.5}"

log() { printf '\033[1;34m[argocd]\033[0m %s\n' "$*"; }

command -v kubectl >/dev/null || { echo "kubectl required" >&2; exit 1; }

if kubectl get crd applications.argoproj.io >/dev/null 2>&1; then
  log "ArgoCD CRDs present — skipping install"
else
  log "installing ArgoCD $ARGOCD_VERSION"
  kubectl create namespace argocd --dry-run=client -o yaml | kubectl apply -f -
  kubectl apply -n argocd --server-side --force-conflicts \
    -f "https://raw.githubusercontent.com/argoproj/argo-cd/$ARGOCD_VERSION/manifests/install.yaml"
fi

# Upstream sets imagePullPolicy=Always on argocd-redis, which bypasses node
# image caches and re-pulls public.ecr.aws on every pod start — ECR Public's
# anonymous data quota is routinely exhausted on shared CI egress IPs
# (ImagePullBackOff), and the whole ArgoCD stack degrades without redis.
# IfNotPresent uses the preloaded image when present (CI side-loads it) and
# pulls once otherwise.
if kubectl -n argocd get deployment argocd-redis >/dev/null 2>&1; then
  # strategic: merges the container entry by name (a JSON merge patch would
  # replace the whole containers list and blank the image field).
  kubectl -n argocd patch deployment argocd-redis --type strategic -p \
    '{"spec":{"template":{"spec":{"containers":[{"name":"redis","imagePullPolicy":"IfNotPresent"}]}}}}'
fi

# Health override for provider-keycloak's ClusterProviderConfig (rendered by
# charts/inari-platform-graphs into the platform-rgd Application): the
# provider only records status.users (usage count) and never writes
# conditions, so ArgoCD's stock *.crossplane.io wildcard health check wedges
# the resource in "Progressing ..." forever — and with it any Application
# that ships one (platform-rgd never became Healthy in install-test).
# Semantics: presence is Healthy (config object); a Ready=False condition,
# if the provider ever writes one, surfaces as Degraded. Validated against
# ArgoCD v3.1.5 via `argocd admin settings resource-overrides health`.
kubectl -n argocd patch configmap argocd-cm --type merge -p "$(cat <<'EOF'
data:
  resource.customizations.health.keycloak.m.crossplane.io_ClusterProviderConfig: |
    hs = { status = "Healthy", message = "Provider config present (provider-keycloak records only status.users)." }
    if obj.status ~= nil and obj.status.conditions ~= nil then
      for _, c in ipairs(obj.status.conditions) do
        if c.type == "Ready" and c.status == "False" then
          hs.status = "Degraded"
          hs.message = c.message or "ClusterProviderConfig not ready"
          return hs
        end
      end
    end
    return hs
EOF
)"

log "waiting for argocd-server"
kubectl -n argocd rollout status deployment/argocd-server --timeout=300s
kubectl -n argocd rollout status deployment/argocd-repo-server --timeout=300s

# Optional: registry credentials for the OCI charts consumed from
# oci://ghcr.io/7k-inari/<repo>/charts. The chart packages are public, so ArgoCD
# pulls anonymously; this secret is only needed while a package is still
# private (e.g. before the first post-move release flips visibility).
if [[ -n "${GHCR_TOKEN:-}" ]]; then
  log "configuring ArgoCD repo credentials for ghcr.io OCI charts"
  kubectl -n argocd apply -f - <<EOF
apiVersion: v1
kind: Secret
metadata:
  name: ghcr-oci-repo
  namespace: argocd
  labels:
    argocd.argoproj.io/secret-type: repository
stringData:
  type: helm
  name: inari-charts
  url: ghcr.io/7k-inari
  enableOCI: "true"
  username: ${GHCR_USERNAME:-github}
  password: ${GHCR_TOKEN}
EOF
fi
log "ArgoCD ready"
