# gitops/e2e/ — persistent e2e environment on the platform cluster

GitOps root for the long-lived e2e environment (architecture plan C4;
contract: [docs/e2e/environment-contract.md](../../docs/e2e/environment-contract.md)).
Synced onto the platform cluster by the pointer Application
`apps/7kgroup/inari-e2e-env.yaml` in `7k-app-of-apps` (path `gitops/e2e/env`,
recurse). Everything here deploys into namespace **inari-e2e** under
AppProject **inari-e2e**.

## Topology

| Hostname (grey-cloud: `cloudflare-proxied=false`) | Serves |
|---|---|
| `https://inari-e2e.7kgroup.org` | console (`/`) + inari-server API (`/api/v1`) — single origin, invariant 3 |
| `https://auth-e2e.7kgroup.org` | dedicated e2e Keycloak, realm `inari` (bundled in platform-config) |
| `https://inari-e2e-gw.7kgroup.org` | agent gateway (ConnectRPC, whole-host routing) |
| `git://git-service.inari-e2e.svc:9418` | in-cluster git service → `E2E_GIT_URL` |

| App (wave) | Contents |
|---|---|
| `inari-e2e-env-extras` (-1) | namespace + ESO secrets (`env-extras/`) |
| `inari-e2e-platform-config` (0) | CNPG Cluster, Keycloak CR + realm import (chart `charts/inari-platform`) |
| `inari-e2e-nats`, `inari-e2e-openfga`, `inari-e2e-git-service` (1) | dependencies |
| `inari-e2e-server`, `inari-e2e-console` (3) | first-party components |

The file values are the **non-HA steady state** (1 replica, single-node
NATS R=1, memory cache, no kubeproxy). Candidate image tags and HA values
are applied per run by the W3 env-prep step; the release gate tests exactly
the run's artifact set, never "whatever is deployed" (invariant 5).

## Image pull path — Harbor `ghcr-proxy`

All `ghcr.io/7k-inari/*` images are pulled through the public Harbor
proxy-cache project **`ghcr-proxy`** on `harbor.7kgroup.org` (upstream
`https://ghcr.io`, no upstream credentials — the org's packages are public).
**Hard rule 7: pull-blocking ("prevent pulling vulnerable images") stays
OFF on this project** — e2e deliberately deploys unscanned edge/old images
for upgrade tests; enforcement lives in the workflow scan gate (W6), never
in the pull path.

## Git service (replaces the kind hostPath coupling)

inari-server runs with `INARI_GIT_PROVIDER=local`,
`INARI_GIT_LOCAL_ROOT=/var/lib/inari/git`, writing real bare repos (go-git)
into the RWX PVC `inari-e2e-git`. The `git-service` Deployment mounts the
same PVC and serves it with `git daemon --export-all` (ClusterIP :9418,
unauthenticated, e2e-internal). Suites clone via `E2E_GIT_URL` instead of
reading a host filesystem. Dumb HTTP cannot serve these repos (no
`update-server-info` ever runs); the git protocol is the minimal correct
choice.

## Prerequisites (one-time user actions BEFORE the env is synced)

1. **Vault KV `inari-e2e/database`** — properties: `superuser-password`,
   `inari-password`, `keycloak-password`, `openfga-password` (random).
   Connection URIs are templated in the ExternalSecret; Vault holds
   passwords only.
2. **Vault KV `inari-e2e/personas`** — properties: `dev-admin-password`,
   `operator-password`, `editor-password`, `viewer-password`,
   `member-password` (consumed by the W3 seed step and suites).
3. **Vault Kubernetes-auth role `inari-e2e-server`** — bound to SA
   `inari-server` in namespace `inari-e2e`; policy: `update` on
   `secret/data/inari/clusters/*` (cluster-registration exchange).
   Caveat: the server hardcodes the `inari/clusters/` prefix, shared with
   the live instance — isolation rests on UUID cluster IDs (accepted,
   architecture plan risk R2 equivalent).
4. **Harbor project `ghcr-proxy`** — public proxy-cache, upstream
   `https://ghcr.io`, no upstream credentials, pull-blocking OFF. Requires
   a Harbor admin credential (Vault `harbor/app`); without the project the
   e2e image values ImagePullBackOff.

DNS needs no action: external-dns creates the records from the HTTPRoute
annotations (same mechanism as the live instance, proxied flag flipped).

### Deliberate exception to "secrets via ESO"

`keycloak.adminClient.secret` in `env/platform-config.yaml` is a committed
random 48-hex value, not Vault-synced: the chart renders it into BOTH the
realm import and the `inari-keycloak-admin` secret, so it must exist at
helm-render time. Blast radius is realm-admin on the throwaway e2e realm
only. A chart knob (`adminClient.existingSecret`) would not fix the
realm-import-time registration; documented as accepted.

## e2e-handoff ConfigMap convention

The W3 seed step resolves the discovered values (Keycloak user id, org ids,
cluster id — the kind-era `Handoff` struct fields) once after every
env-prep and publishes them as ConfigMap **`e2e-handoff`** in namespace
`inari-e2e` (keys: `namespace`, `tenant`, `kc_uid`, `org_kc_id`,
`cluster_id`, `org_id`; the kind-era `git_host_dir`/`agent_chart_dir` are
gone — replaced by `E2E_GIT_URL` / the env's fixed charts). Suites read the
ConfigMap instead of a handoff JSON file; `E2E_SEEDED=true` gates every
suite leg (contract §1.1).

## Verification (P0 exit criteria)

1. All seven apps Synced + Healthy (incl. the chart's PostSync
   realm-verify job) — `argocd app list -p inari-e2e`.
2. `curl https://inari-e2e.7kgroup.org/config.js` → 200;
   `curl -o /dev/null -w '%{http_code}' https://inari-e2e.7kgroup.org/api/v1/tenants`
   → 401. (No public health endpoint is routed: inari-server's `/healthz`
   is server-root only while the HTTPRoute matches prefix `/api`, so a 401
   from a real API operation is what proves route + server + auth
   middleware.)
3. `curl https://auth-e2e.7kgroup.org/realms/inari/.well-known/openid-configuration`
   → 200 with `issuer` == `https://auth-e2e.7kgroup.org/realms/inari`,
   from a runner pod (W3) or any in-namespace pod.
4. Pods run `harbor.7kgroup.org/ghcr-proxy/…` images; pulling a
   not-yet-cached tag through the proxy returns 200 (pull-through works).
5. `dig +short inari-e2e.7kgroup.org` returns the origin IP, not
   Cloudflare (grey-cloud confirmed).
6. `git ls-remote git://git-service.inari-e2e.svc:9418/` reachable from a
   pod in the namespace.

## Ownership / boundaries

- `gitops/e2e/env/`, `env-extras/`, `git-service/` — this task (W2).
- `gitops/e2e/argo-workflows/` (WorkflowTemplates, CronWorkflows, runner
  RBAC) — W3, synced by its own pointer (destination `inari-e2e`).
- CNPG barman backup wiring for the nightly-reset DR drill lands in
  `env-extras/` with the W3 reset CronWorkflow.
