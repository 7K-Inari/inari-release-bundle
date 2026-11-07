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

## Harbor scan gate (W6)

Release-mode runs (`mode=release`) scan exactly the candidate image set
(hard rule 5) with Trivy via the Harbor API before any deploy: the
`harbor-scan-gate` DAG leg runs parallel with `cosign-verify` and blocks
`env-prep`. Per image (`inari-server`, `inari-ui-bundle`, `inari-agent` in
`ghcr-proxy`) the driver ([`scripts/e2e/scan/`](../../scripts/e2e/scan/)):

1. **Primes the proxy cache** with a manifest-only pull — a proxy-cache
   artifact exists only after something pulls it through.
2. **Triggers the scan explicitly** (`POST
   /api/v2.0/projects/ghcr-proxy/repositories/<repo>/artifacts/<tag>/scan`)
   and polls `?with_scan_overview=true` until `scan_status=Success` —
   asynchronous; a prior scan is never trusted (scan-on-push latency races).
   `Error`/`Stopped` gets exactly one retrigger.
3. **Archives the full report** (`additions/vulnerabilities`) in the Garage
   bucket at `e2e/<workflow-uid>/scan/reports/`.
4. **Fails the run** when the Critical count exceeds the workflow parameter
   `scan-critical-threshold` (default `0`), and always writes the synthetic
   **`security` JUnit** (`e2e/<uid>/scan/junit.xml` → the run's ReportPortal
   launch via the exit-handler, including on failure — hard rule 8/10).

Policies that live in **Harbor, never in scripts**: CVE allowlists (project
`ghcr-proxy` → CVE Allowlist), scan-on-push, the daily re-scan schedule. The
gate reads Harbor's own severity summary, so Harbor-side allowlisting is the
single exemption knob. Complements cosign (provenance), never replaces it.

### Prerequisites (one-time owner actions)

1. **Robot account** — Harbor UI → project `ghcr-proxy` → Robot Accounts →
   `e2e-scan-gate`, no expiration, least privilege: **Pull Repository, List
   Repository, Read Repository (Artifact), Create Scan** (the existing Vault
   `harbor/robot` is pull-only and stays untouched).
2. **Vault** — `vault kv put secret/inari-e2e/harbor-robot
   username='robot$ghcr-proxy+e2e-scan-gate' token='<token>'`. Synced by
   `argo-workflows/harbor-scanner-secret.yaml` (ESO, hourly; force via a
   `force-sync` annotation on the ExternalSecret). A missing secret fails the
   release run loud (config-error JUnit) — it never silently skips.
3. **Scan-on-push** — project `ghcr-proxy` → Configuration → enable
   "Automatically scan images on push" (`auto_scan`). Runtime DB config, not
   GitOps-able.
4. **Daily re-scan** — Administration → Interrogation Services →
   Vulnerability → schedule "Scan all" **Daily** (`scan_all_policy`) so
   cached artifacts pick up newly published CVEs between releases.

### Troubleshooting

| Symptom | Likely cause |
|---|---|
| `401`/`403` on the API calls | robot scopes — needs Create Scan + read/pull on `ghcr-proxy`; check Vault entry vs Harbor robot after rotation (ESO refresh/force-sync) |
| `artifact not registered … after re-priming` | the tag does not exist upstream on `ghcr.io`, or the manifest pull 404s — check the tag |
| persistent `scan ended in status "Error"` | Trivy scanner broken — usually vulnerability-DB egress from the cluster |
| release run fails with `scan-gate-configuration` | prerequisite 2 not done / ESO not synced |
| slow first scan of a large image | normal — the leg polls up to 10 min per image (`activeDeadlineSeconds: 1500` overall) |

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
2. **Vault KV `inari-e2e/personas`** — properties: `dev-admin`, `operator`,
   `editor`, `viewer`, `member`, `ops` (consumed by the W3 seed step and
   suites; the ExternalSecret maps them onto `-password`-suffixed target
   keys). `ops` is the janitor's e2e-ops approver identity (E2E_OPS_PASSWORD);
   the remoteRef is non-optional, so a missing property fails the WHOLE
   personas secret.
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
5. **ReportPortal importer credential** (reporting only — runs work without
   it, the onExit import just skips): on https://reportportal.7kgroup.org
   (superadmin) create project **`inari-e2e`**, create local user
   **`e2e-importer`** assigned to it (MEMBER suffices), generate an API key,
   then `vault kv put secret/inari-e2e/reportportal api-key=<uuid>`. Synced
   by `argo-workflows/reportportal-importer-secret.yaml` (ESO, hourly; force
   via a `force-sync` annotation on the ExternalSecret).
6. **Harbor scan-gate robot** (release gate only — runs without it fail loud
   at the scan leg): see §Harbor scan gate → Prerequisites (robot account +
   Vault `inari-e2e/harbor-robot` + scan-on-push + daily re-scan).

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
`cluster_id`, `org_id`, `agent_namespace` — the agent chart's namespace,
from `E2E_AGENT_NAMESPACE`, so suites never hardcode `inari-system` —
`toolbox` — the curl-transport pod the Go suite
execs into, see `env-extras/toolbox.yaml`; the kind-era
`git_host_dir`/`agent_chart_dir` are gone — replaced by `E2E_GIT_URL` / the
env's fixed charts). Suites read the ConfigMap instead of a handoff JSON
file; `E2E_SEEDED=true` gates every suite leg (contract §1.1).

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

## Argo Workflows plane (`argo-workflows/` + `chainsaw-hooks/`)

Synced by the **second pointer app** (`apps/7kgroup/inari-e2e-workflows.yaml`
in 7k-app-of-apps, path `gitops/e2e/argo-workflows`, destination
`inari-e2e`). Test DEFINITIONS only — the Argo Workflows install itself
lives in 7k-app-of-apps (W1).

| File | Contents |
|---|---|
| `workflowtemplate-e2e-stack.yaml` | The one parameter-driven workflow (contract §3.1): resolve → cosign-verify (release) + harbor-scan-gate (release, W6 — Trivy scan of the candidate images via Harbor, fail on Critical > `scan-critical-threshold`) → [upgrade mode: resolve-previous → env-prep-previous → upgrade-fixture → drill-pause] → env-prep → seed → agent-ensure → suite legs (ginkgo / playwright×2 shards / apischema on release, upgrade-assert on upgrade) + chainsaw-verify; onExit imports every suite's JUnit into ReportPortal as ONE launch per run (name = `reportportal-launch`, attributes mode/run-id/image tags/charts-ref/status; a synthesized run-outcome testsuite anchors aborted runs — hard rule 8/10) |
| `cronworkflow-upgrade.yaml` | **Suspended** daily (05:47 UTC) `mode=upgrade` run — the upgrade-path leg (W5). Unsuspension criterion in the file header: first post-sunset stable inari-server release, then ~2 weeks stable before any release-gating flip |
| `reportportal-importer-secret.yaml` | ESO ExternalSecret syncing the ReportPortal API key (Vault `inari-e2e/reportportal`) for the onExit importer; optional reference — runs skip the import with a warning until the owner provisions it |
| `harbor-scanner-secret.yaml` | ESO ExternalSecret syncing the Harbor scan-gate robot credentials (Vault `inari-e2e/harbor-robot`); the scan gate fails LOUD until the owner provisions it — a release gate never silently skips (§Harbor scan gate) |
| `workflowtemplate-e2e-janitor.yaml` + `cronworkflow-janitor.yaml` | Reap `e2e-*` orgs older than retention-days (contract invariant 6), skipping `e2e-org` + active runs; every 6h. Two identities: requests as dev-admin, approves as **e2e-ops** (lifecycle approvals are never self-decidable) |
| `cronworkflow-nightly.yaml` | 03:17 UTC full surface incl. quarantined, edge images, env-wide HA flip |
| `cronworkflow-weekly.yaml`, `cronworkflow-nightly-reset.yaml` | **Suspended placeholders** (phase 2: load/chaos legs; restore-from-backup needs CNPG barman wiring in env-extras first) |
| `semaphore-e2e-env-lock.yaml` | One permit — serializes every run against the single env |
| `rbac.yaml` | Runner SAs (e2e-runner default; e2e-seed, e2e-agent-ensure, e2e-chainsaw-hook per step; e2e-submitter for the GHA wrappers), the env-prep Application-patch Role in ns argocd, the controller semaphore-write fix, and the e2e agent namespace + cluster RBAC |
| `env-contract-configmap.yaml` | The non-secret half of the env contract, envFrom'd into every leg |
| `artifact-repo-credentials.yaml` | GarageKey + GarageReferenceGrant copying the artifact-repo S3 creds into inari-e2e (artifact secrets resolve in the workflow's namespace) |
| `../chainsaw-hooks/smoke-job.yaml` | PostSync chainsaw smoke Job, attached to the inari-e2e-server app as a second source — every candidate flip is smoke-gated. **Deviation (Q9):** only chainsaw runs as a PostSync hook; the taxonomy §3 Ginkgo/Playwright smoke sets run via `mode=smoke` submissions |

Env-prep mutates the child Applications (`inari-e2e-server` /
`inari-e2e-console` / `inari-e2e-nats`) by JSON patch —
`targetRevision` + `helm.valuesObject`. **ArgoCD never merges
`helm.values` with `helm.valuesObject`** (`ValuesYAML()` returns one XOR
the other, argo-cd v3.3.8 values.go), so the env files carry their
baselines as `valuesObject` and every env-prep patch writes the COMPLETE
desired object (baseline + per-run flip), never a delta. The env pointer
app carries `ignoreDifferences` for exactly those fields, so the patches
persist for the run's duration and each run re-converges the env to its
own artifact set (hard rule 5). Platform apps are health-checked, never
mutated.

**NATS is permanently 3-node clustered** (since 2026-11-07): JetStream
cannot convert a standalone-born PVC to clustered in place (no meta
leader — the first nightly died on exactly this), so `env/nats.yaml` and
env-prep both pin `cluster.enabled: true, replicas: 3` and the HA flip
covers server/console/agent only.

Manual run: `argo submit --from workflowtemplate/e2e-stack -n inari-e2e -p
mode=smoke` (identity: platform-admin SSO, or the e2e-submitter token). The
token must be the FULL Authorization header — the argo CLI sends
`ARGO_TOKEN` verbatim, so a bare JWT fails with "token not valid"; harvest
with `printf 'Bearer %s' "$(kubectl -n inari-e2e get secret
e2e-submitter-token -o jsonpath='{.data.token}' | base64 -d)"`. Every
run — succeeded, failed, or aborted — lands in ReportPortal project
`inari-e2e` as one launch (`https://reportportal.7kgroup.org/ui/#inari-e2e/launches/all`);
JUnit XMLs also persist in the Garage bucket at `e2e/<workflow-uid>/`.

### Upgrade leg (`mode=upgrade`, W5)

The highest-value phase-2 extension (plan §10): catches the DB-migration
breakage class (cf. migration-0029 — a candidate migration breaking on data
written by the previous version). One run:

1. `resolve-previous` resolves the previous stable artifact set: latest
   stable release git tag per chart (`inari-server-vX.Y.Z` /
   `inari-console-vX.Y.Z` — byte-identical to the published OCI charts;
   strict semver only, edge `-<sha>` tags never match) + that tag's
   `appVersion` as the image. **Fallback while no stable inari-server chart
   release exists:** the server chart falls back to `charts-ref` and the
   image to the latest stable ghcr tag. `upgrade-from-server-image` /
   `upgrade-from-console-image` parameters pin the baseline explicitly
   (drills, debugging).
2. `env-prep-previous` deploys that set (non-HA), same patch/wait/probe
   machinery as the candidate env-prep.
3. `upgrade-fixture` creates a tenant org **written by the old version**
   (`e2e-<uid>-upgrade` — reaped by the janitor like any suite org). The
   full Go seed does NOT run on the old stack: the latest stable server
   (3.0.0) predates the roles/membership APIs it exercises.
4. Candidate `env-prep` → `seed` → `agent-ensure` → the suite legs re-run
   the **gate selection** (taxonomy §3) against the upgraded stack.
5. `upgrade-assert` proves the fixture tenant survived (read-only) and emits
   the leg's synthetic JUnit (`upgrade` testsuite → the run's ReportPortal
   launch; `e2e/<uid>/upgrade/junit.xml` in S3).

Migration semantics that make this safe on a shared env (goose v3,
empirically verified against v3.29.0): an old binary treats a **newer**
database as a no-op (`Up()` ignores applied versions absent from its
filesystem), and failed migrations roll back transactionally — there is no
golang-migrate-style dirty flag, so a broken-migration run does not poison
the env. All 39 migrations run in transactions (no `NO TRANSACTION`).

**Non-gating**: nothing submits `mode=upgrade` from a gate. The
`e2e-upgrade` CronWorkflow ships **suspended**; unsuspend it once the first
*post-sunset* stable inari-server release ships (the pre-sunset 3.0.0
baseline cannot serve the fixture: no roles/membership APIs, and the
operator CRDs it provisioned through are gone). The release-gating flip —
a second `argo submit --wait` with `mode=upgrade` in `release-e2e.yaml` —
is a deliberate follow-up after ~2 weeks of stable runs.

**Broken-migration drill** (acceptance + future drills). Two rules first:

- **The previous and candidate images must differ.** Same-image "upgrades"
  produce no rollout, so goose never re-runs and a sabotage would go
  undetected. Any two post-sunset edge tags work (e.g. previous
  `4.0.0-282b7a5`, candidate `4.0.0-fc36dbf`); published-image pairs
  straddling a real migration boundary only exist once a new migration
  lands on inari-server main (the legs then exercise it for real).
- Schedule drills clear of the 03:17Z nightly window.

Submit with a deterministic sabotage window:

```
argo submit --from workflowtemplate/e2e-stack -n inari-e2e -p mode=upgrade \
  -p upgrade-from-server-image=<prev> -p upgrade-from-console-image=<prev> \
  -p server-image=<candidate> -p server-src-ref=<sha> \
  -p upgrade-drill-pause-minutes=15
```

During the pause, sabotage the candidate's migration re-run — e.g. force
migration `0039` to re-execute against a pre-claimed index name (adjust the
version/object to the current max migration):

```
primary=$(kubectl -n inari-e2e get pod -l cnpg.io/cluster=postgresql,cnpg.io/instanceRole=primary -o jsonpath='{.items[0].metadata.name}')
kubectl -n inari-e2e exec "$primary" -c postgres -- psql -U postgres -d inari -c "
  DROP INDEX tenant_git_repos_repo_provider_key;
  DELETE FROM goose_db_version WHERE version_id = 39;
  CREATE INDEX tenant_git_repos_repo_provider_key ON tenant_git_repos (repo);"
```

The candidate server then crashloops on migration 0039 (`relation ...
already exists`) → the app never goes Healthy → `env-prep` fails the run;
the ReportPortal launch lands FAILED with the run-outcome anchor. Expected
failure signature in the server logs:
`db: migrate: ... partial migration error (type:sql,version:39)`.

**Benign variant** (proves migrations *execute* on the upgrade path and
succeed): same procedure minus the decoy `CREATE INDEX` — the candidate
re-applies 0039 cleanly and the leg goes green.

**Recovery** (no poisoned state — goose rolled the failed migration back):
drop the decoy (`DROP INDEX tenant_git_repos_repo_provider_key;`) and let
the next run re-converge; the candidate re-applies 0039 cleanly.

## Ownership / boundaries

- `gitops/e2e/env/`, `env-extras/`, `git-service/` — the env task (W2).
- `gitops/e2e/argo-workflows/`, `chainsaw-hooks/` — the workflows task (W3b),
  synced by its own pointer (destination `inari-e2e`).
- CNPG barman backup wiring for the nightly-reset DR drill lands in
  `env-extras/` together with the (currently suspended) reset CronWorkflow.

