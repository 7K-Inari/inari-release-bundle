# scripts/e2e — end-to-end suite map

> **Migration contracts:** the target environment contract and label taxonomy
> for the live-env migration are normative in
> [`docs/e2e/environment-contract.md`](../../docs/e2e/environment-contract.md)
> and [`docs/e2e/test-taxonomy.md`](../../docs/e2e/test-taxonomy.md). This
> README documents the current kind-era implementation; the docs above record
> it as the migration baseline.

Canonical home of the Inari e2e suites (relocated from the inari-server
repo's `e2e/` directory). `.github/workflows/release-e2e.yaml` is the CI
gate: a thin wrapper that resolves the candidate artifact set and submits
the Argo WorkflowTemplate `e2e-stack`
(`gitops/e2e/argo-workflows/workflowtemplate-e2e-stack.yaml`) on the
persistent e2e environment — the stack leg (env re-convergence → seed →
suite legs → JUnit → ReportPortal) lives in the cluster. The kind-era
reusable GHA workflow `.github/workflows/e2e-stack.yaml` is retired.
Docs/markdown-only changes pushed to a Release PR
intentionally skip the e2e jobs (the workflow's `gate` job detects them and
reports success) — e2e "not running" on such pushes is expected, not a CI
outage. Suites are organized by surface (per the approved e2e
architecture plan §1, "Unified testing folder structure"):

```
scripts/e2e/
├── ui/                       # console UI e2e (Playwright Test suite; see ui/README.md)
├── api/
│   └── api_schema_e2e_test.go    # API ↔ OpenAPI schema conformance (Go, tag e2e)
├── scan/                       # Harbor/Trivy scan-gate driver (Go module) — the release-gate
│                               #   harbor-scan-gate leg on the e2e-stack WorkflowTemplate (W6)
├── load/                       # k6 load scenarios (flat: common.js + k6-summary.js + 3 scenarios +
│                               #   testrun-template.yaml) — the weekly k6-run legs via k6-operator (W7;
│                               #   NEVER in PR/release gates)
├── kubectl/
│   ├── kubectl-access.sh         # control-plane-only kubectl access scenario
│   └── kubectl-tunnel.sh         # gateway-mode chain: kubeproxy → tunnel-agent → apiserver
├── lib/
│   └── ui-proxy.mjs              # single-origin shim shared by the UI suites
└── stack/                      # golden-path suite — provisions AND asserts (self-contained Go
    │                           #   module, tag e2e; shell→Go migration complete, golden-path.sh
    │                           #   deleted in phase 3)
    ├── ginkgo_test.go            # TestGoldenPath: Ginkgo bootstrap (RunSpecs + BeforeSuite Load —
    │                           #   in-process bring-up or handoff attach); per-phase assertion specs
    │                           #   live in tenant/cluster/rbac/policy/disruption _test.go, labeled
    │                           #   with the canonical taxonomy (suite/labels.go)
    ├── renderpath_test.go        # M5W2+M5W3-2 render-path validation incl. the provider-keycloak
    │                           #   phases (Label "renderpath", manual-only)
    ├── provision_test.go         # TestProvisionStack/TestProvisionAgent (tag e2eprovision —
    │                           #   targeted chart-install reruns, excluded from the plain e2e run)
    ├── suite/                    # provide.go (kind/hostPath/operators) + provision.go (helm layers)
    │                           #   + seed.go (KC seeding, tenant/cluster/agent) + tenant / cluster /
    │                           #   RBAC / policy / disruption (E2E_HA=1) assertion phases
    ├── internal/                 # thin wrappers: poll, kube, kind, kc, inariapi, fga, helm (CLI exec only)
    └── testdata/                 # e2e values: nats-values.yaml (HA 3-node), nats-values-e2e.yaml
                                #   (non-HA single node), platform/server-values-e2e[-ha].yaml trims
```

## Suite map

| Suite | Path | Runner | CI trigger | Gating |
|---|---|---|---|---|
| Golden path (fast leg) | `stack/` (Go/Ginkgo suite, `E2E_HA=0`) | `go test -tags=e2e -count=1 -timeout 40m -json ./... -args "--ginkgo.label-filter=(p0\|\|smoke)&&!quarantine" --ginkgo.junit-report=junit.xml` from `scripts/e2e/stack/` (nested module — won't resolve from the repo root). Locally the suite provisions the whole kind stack in-process, then asserts; on the e2e env it attaches to the seed's `e2e-handoff` ConfigMap (`E2E_HANDOFF_CONFIGMAP`) instead. Quarantined Go specs (`Label("quarantine")`) are excluded by the label filter. | release gate: `release-e2e.yaml` submits the Argo WorkflowTemplate `e2e-stack` (`mode=release`, gate label selection) on the persistent e2e environment | **Yes** — `release-e2e.yaml` waits on the Argo run |
| Golden path (HA leg) | `stack/` (Go/Ginkgo suite, `E2E_HA=1` flips helm replica values + unlocks disruption specs) | same, with `E2E_HA=1` + `E2E_MIGRATIONS_DIR` (`SERVER_MIGRATIONS_DIR` kept as a legacy alias) | the Argo CronWorkflow `e2e-nightly` runs HA; the release gate submits `ha=true`; otherwise manual `argo submit -p ha=true` | Yes on release runs (gate), non-blocking in the nightly |
| Render-path validation (M5W2 + M5W3-2 keycloak, operator sunset) | `stack/renderpath_test.go` + `stack/suite/renderpath.go` (Ginkgo Ordered specs, `Label("renderpath")` — no priority label, so the gate/nightly filters never select them) | handoff-attached to a provisioned stack: provision with `KEEP_CLUSTER=true E2E_PROVISION_ONLY=1 E2E_HANDOFF_PATH=… go test -tags=e2e ./...` from `scripts/e2e/stack/`, then `E2E_HANDOFF_PATH=… go test -tags=e2e . -args --ginkgo.label-filter=renderpath` | none — manual validation suite | No — manual only |
| API schema conformance | `api/api_schema_e2e_test.go` | `go test -tags=e2e` inside the inari-server checkout at the pinned tag | Argo WorkflowTemplate `apischema` task (`mode=release` only, after the seed) | Yes — release gate only |
| Console UI e2e | `ui/` (Playwright Test) + `lib/ui-proxy.mjs` + `ui/seed/seed-personas.mjs` | `npx playwright test --config playwright.config.ci.ts --grep @p0 --grep-invert @quarantine` in `ui/` | Argo WorkflowTemplate `playwright` tasks (2 shards, every mode except `weekly`) | Yes on release runs (gate), non-blocking otherwise |
| Golden path (nightly, incl. quarantined Go specs) | `stack/` (Go/Ginkgo suite) | same as the fast leg, but the label filter is `p0\|\|p1\|\|p2` so `Label("quarantine")` specs run | Argo CronWorkflow `e2e-nightly` (`gitops/e2e/argo-workflows/cronworkflow-nightly.yaml`, `mode=nightly`, cron 03:17 UTC + manual `argo submit`) | No — nightly only, non-blocking |
| Console UI e2e (full, incl. quarantine) | `ui/` (Playwright Test) | `npx playwright test --config playwright.config.ci.ts --grep "@p0\|@p1\|@p2"` (includes `@quarantine`, `continue-on-error`) | same Argo CronWorkflow `e2e-nightly` run — single env re-convergence, playwright legs run the full tag set | No — nightly only, non-blocking |
| Upgrade path (previous stable → candidate) | reuses `stack/` + `ui/` gate selections on the upgraded env; the leg itself lives in `gitops/e2e/argo-workflows/workflowtemplate-e2e-stack.yaml` (`mode=upgrade`: resolve-previous → env-prep-previous → fixture → candidate env-prep → suites → upgrade-assert) | Argo workflow on the live env; Ginkgo `(p0\|\|smoke)&&!quarantine` + Playwright `--grep "@p0\|@smoke" --grep-invert @quarantine` + fixture-survival JUnit | `cronworkflow-upgrade.yaml` (**suspended** until the first post-sunset stable inari-server release) + manual `argo submit -p mode=upgrade`; drill runbook in `gitops/e2e/README.md` | No — non-gating; release-gating flip is a follow-up after ~2 weeks stable |
| Harbor image scan gate (W6) | `scan/` (Go driver: prime proxy cache → trigger → poll → threshold → report) + the `harbor-scan-gate` leg in `gitops/e2e/argo-workflows/workflowtemplate-e2e-stack.yaml` | `go run .` in `scan/` (unit tests: `go test ./...`); on the cluster the leg runs it against Harbor's in-cluster core service (project `ghcr-proxy`) | the `e2e-stack` WorkflowTemplate in `mode=release` (parallel with cosign-verify, before env-prep); emits the `security` JUnit + full CVE reports in S3 | **Yes** — fails the release run when any candidate image exceeds `scan-critical-threshold` (default 0) Critical CVEs |
| Load suite (W7) | `load/` (k6 scenarios: permissions-cache, login-storm, provisioning-outbox — flat layout, vendored k6-summary; see `load/README.md`) + the `k6-run` legs in the e2e-stack WorkflowTemplate (`mode=weekly`) | k6-operator TestRuns on the e2e env (`harbor.7kgroup.org/ghcr-proxy/grafana/k6-operator` runner); locally any `k6 run` with the contract env vars | `cronworkflow-weekly.yaml` (**suspended** until the W7 acceptance run) — Saturday 02:17 UTC, ~23h ahead of the nightly reset; manual: `argo submit --from workflowtemplate/e2e-stack -n inari-e2e -p mode=weekly -p load-duration=2m` | **No — never in PR or release gates** (hard rule 6); threshold breaches fail the weekly run (k6 nonzero exit) and land in ReportPortal |
| kubectl access | `kubectl/kubectl-access.sh` | bash script (docker etcd + kube-apiserver; no kind) | not wired into CI yet | No — manual scenario |
| kubectl tunnel (gateway) | `kubectl/kubectl-tunnel.sh` | bash script (docker etcd + kube-apiserver + Keycloak + postgres + OpenFGA + kubeproxy + tunnel-agent; no kind). Builds `inari/kubeproxy:e2e` and `inari/agent:e2e` from sibling checkouts (`INARI_SERVER_DIR`/`INARI_AGENT_DIR`); asserts impersonation, 503 upgrade path, 410 kill-switch, per-cluster client revoke, max-lifetime reaper | `e2e-nightly.yaml` job `kubectl-tunnel` (builds inari-server at the resolved tag + inari-agent main) | No — nightly only, non-blocking |

## Workflow call graph

```
release-e2e.yaml  (Release-PR gate, thin wrapper: gate → resolve → submit/wait
                   + server-integration; submits Argo WorkflowTemplate
                   e2e-stack mode=release and posts RP/Argo links to the PR)

edge.yml          (per-merge edge releases; e2e-trigger submits e2e-stack
                   mode=edge fire-and-forget — skipped on docs-only merges)

Argo (cluster, ns inari-e2e)
├── CronWorkflow e2e-nightly   (03:17 UTC, mode=nightly, full surface
│                               incl. quarantined, HA)
├── CronWorkflow e2e-weekly    (suspended — no load/chaos legs yet)
└── CronWorkflow e2e-upgrade   (suspended until the first post-sunset
                                stable inari-server release)

e2e-nightly.yaml  (GHA remainder, cron + dispatch, non-blocking — no cluster
                   legs: quarantine-sweep (ReportPortal-fed: files/expires
                   e2e-flake issues + the pinned quarantine budget;
                   issues:write, no cluster) + the docker-only kubectl-tunnel
                   scenario)
```

Artifacts live in the Garage S3 bucket (`argo-workflows-artifacts`,
`e2e/<workflow-uid>/...` — JUnit per leg, env-prep summaries, logs) with
bucket-lifecycle retention; the onExit handler imports every leg's JUnit
into ReportPortal as one launch per run.

## Flakiness containment

The gate NEVER runs a quarantined test — the direct analog of inari-server's
`flaky` build-tag convention.

- **Gate command** (Argo `e2e-stack` `mode=release`, submitted by
  `release-e2e.yaml`):
  `npx playwright test --config playwright.config.ci.ts --grep @p0 --grep-invert @quarantine`.
  CI runs with `retries: 2`, so a test that passes only after a retry is
  green in the gate but recorded as a flake candidate.
- **Flake history** lives in ReportPortal (project `inari-e2e`): every suite
  leg's JUnit imports as one launch per run (name `<mode>-<uid>`, attribute
  `mode:nightly`), so a test passing only after a retry shows as mixed
  FAILED+PASSED items and a recurring failure as all-FAILED across launches.
  (`ui/report-flakes.mjs` + the `ui-e2e-flakes-*` artifacts were retired with
  the ReportPortal feed.)
- **Quarantine policy** (manual part): when an `e2e-flake` issue is triaged,
  tag the test `@quarantine` in its spec title. Quarantined tests are
  excluded from the gate (`--grep-invert @quarantine`) and run only in the
  nightly. De-quarantine (remove the tag, close the issue) once the test
  has been stable in the nightly for ~2 weeks.
- **Quarantined Go specs** (stack suite): label the spec
  `Label("quarantine")` (see `stack/suite/labels.go`), with the issue
  number/reason in a comment. The release gate's label filter
  (`(p0||smoke)&&!quarantine`) never selects it; the nightly filter
  (`p0||p1||p2`) always does — the label analog of inari-server's `flaky`
  build tag. De-quarantine by removing the label once stable in the nightly
  for ~2 weeks.
- **Quarantine automation** (`ui/quarantine-issues.mjs`, nightly only —
  docs/e2e/test-taxonomy.md §4): queries ReportPortal for tests unstable
  (flaky or recurring-failure) in 2+ nightly launches within 7 days and
  files/dedupes issues titled `[flake]|[failure] <test path> — <test title>`
  labeled `e2e-flake` (an open issue for the same test gets a comment with
  the new occurrences). It also enforces the **expiry policy** (14 days
  without human activity ⇒ warning comment; 30 days ⇒ escalation comment +
  `quarantine-overdue` label — fix, re-scope, or delete the test) and
  maintains the pinned `[quarantine-budget]` issue with the
  quarantined-test count (budget 10). Auth: read-only `REPORTPORTAL_API_KEY`
  repo secret (RP user `e2e-sweeper`, CUSTOMER role — owner action, see
  `gitops/e2e/README.md` §Prerequisites); `DRY_RUN=1` plans without
  mutating GitHub. ReportPortal visualizes, GitHub issues act.

## Running locally

### Golden path (kind full stack)

Prereqs: `docker`, `kind`, `kubectl`, `helm`, `jq`, `git`, Go, plus a checkout
of the `inari-agent` repo next to this one (or set
`AGENT_CHART_DIR` explicitly). Build the e2e images
first (`inari/server:e2e` from inari-server, `inari/agent:e2e` from
inari-agent). For the HA assertions also set `E2E_MIGRATIONS_DIR` to the
inari-server checkout's `internal/db/migrations`.

One command (shell→Go migration phase 3 — `golden-path.sh` is deleted):
the Go/Ginkgo stack suite provisions the whole stack in-process (kind
cluster + git hostPath → operators → helm charts in dependency layers → KC
seeding → tenant/cluster/agent) and then asserts:

```sh
cd scripts/e2e/stack
KEEP_CLUSTER=true E2E_HANDOFF_PATH=/tmp/inari-e2e-handoff.json \
  go test -tags=e2e -count=1 -timeout 40m ./...    # add E2E_HA=1 + E2E_MIGRATIONS_DIR for the HA leg
```

`KEEP_CLUSTER=true` keeps the kind cluster and the git host dir alive after
the run (for the api-schema/UI suites or inspection); without it the suite
tears both down at test end. Pointing `E2E_HANDOFF_PATH` at an EXISTING
handoff file skips provisioning entirely and asserts against that
already-running stack (fast local reruns). `-timeout` must exceed go
test's 10m default — bring-up + assertions take longer.

The suite is Ginkgo v2: specs carry the canonical labels
(`stack/suite/labels.go` — one priority label `p0|p1|p2` each, plus `smoke`/
surface labels and `quarantine`; the `renderpath` manual-validation specs
are the deliberate no-priority-label exception, selected only via
`--ginkgo.label-filter=renderpath`). Select subsets with
`-args --ginkgo.label-filter=...` (e.g. `"(p0||smoke)&&!quarantine"` for the
release gate, `p0||p1||p2` for the nightly incl. quarantine) and emit JUnit
with `--ginkgo.junit-report=junit.xml`. On the live e2e environment the
suite attaches via `E2E_HANDOFF_CONFIGMAP=<namespace>/e2e-handoff` (the
ConfigMap the seed step writes) instead of provisioning kind.

Env knobs: the full kind-era variable reference (defaults, semantics, and
the old→new mapping) is maintained as the migration baseline in
[`docs/e2e/environment-contract.md` §2.1](../../docs/e2e/environment-contract.md)
— this README intentionally does not duplicate it.

## CI caching (retired — kind-era GHA only)

The reusable GHA workflow `e2e-stack.yaml` that owned these caches is
**retired** (the stack leg moved to the Argo WorkflowTemplate on the
persistent e2e environment, which pulls through the Harbor proxy-cache —
no image tars, no GHA cache keys). The notes below are kept as historical
reference for the local kind loop; `stack/testdata/images.txt` still pins
the images/versions the local suite uses.

| Cache | Key | Invalidates when |
| --- | --- | --- |
| Image tar (`/tmp/e2e-images/images.tar`) | `e2e-images-{os}-{hash(images.txt)}` (no restore-keys) | any pin in `images.txt` changes |
| Helm repo config + index (`~/.config/helm` + `~/.cache/helm`) | `helm-{os}-{hash(images.txt)}` (+ soft `helm-{os}-` restore-key) | chart pin bump (manifest records pins) |
| Go modules | setup-go hash of `stack/go.sum` (`cache-dependency-path`) | stack dep bump |
| Playwright browsers | `playwright-{os}-{ImageVersion}-{hash(ui package-lock.json)}` | ui lockfile or GH runner image bump |
| Buildx layers (inari-agent, repair-path inari-server) | GHA cache scopes `inari-agent` / `inari-server` | automatic per-layer |

**Image preload mechanism**: a single `docker save` tar of every image in
`stack/testdata/images.txt` (kind node image, CNPG operator + postgres
operand, keycloak + operator, NATS trio, OpenFGA, Vault, ESO, HA redis,
console nginx/oras, curl). On a cache hit the workflow `docker load`s the
tar (for the `kindest/node` image), pre-creates the cluster with
`kind create`, and runs one `kind load image-archive` (targets all nodes —
HA-leg safe); the Go suite then ADOPTS that cluster (`E2E_ADOPT_CLUSTER=1`,
`GIT_HOST_DIR=/tmp/inari-e2e-git`) instead of recreating an empty one. On a
miss all images are pulled in parallel (`xargs -P8`) and saved to the tar.
Chosen over a runner-local registry + containerd mirror: fewer moving
parts, no mirror plumbing in the suite's kind config, and identical
behavior on the HA leg. Chart installs force
`imagePullPolicy: IfNotPresent` via e2e-only values/`--set` overrides
(never production defaults) so preloaded images are used as-is, and the
rendered image refs must match the cached names EXACTLY — containerd
image identity is the full ref string, so the HA redis set pins
`redis.image.registry=docker.io` (the bitnami subchart's default
`registry-1.docker.io` would silently miss the preload and re-pull).
The kind node image is passed to `kind create` tag-only: `docker load`
does not restore RepoDigests, so a `repo:tag@digest` inspect fails on
the loaded image and kind would re-fetch the manifest from Docker Hub
on every hit. On a helm cache hit the workflow sets
`HELM_REPOS_SEEDED=1` and repo add/update are skipped — otherwise they
re-fetch every index each run and the helm cache saves nothing.

**Excluded from the tar** (would break cache correctness): the per-release
`ghcr.io/7k-inari/inari-server:<tag>` (changes every run — pulled +
cosign-verified per job) and `inari/agent:e2e` (built in-job — the buildx GHA
cache covers it).

**Regenerating the manifest**: bump the pin (chart `--version` in
`stack/suite/provision.go`, `defaultKindNodeImage` in
`stack/suite/provide.go`, `CNPG_VERSION`/`KC_VERSION` in
`scripts/install-operators.sh`, or a chart values image), update the
matching pin at the top of `stack/testdata/gen-images.sh`, then run
`stack/testdata/gen-images.sh > stack/testdata/images.txt` and review the
diff. The new file hash becomes the new cache key — no manual cache
busting needed.

### API schema conformance

Requires a running golden-path stack (keep it up with `KEEP_CLUSTER=true`)
and an inari-server checkout. The driver imports inari-server modules and
reads `dist/openapi.yaml`, so copy it into the checkout and run it there:

```sh
cp scripts/e2e/api/api_schema_e2e_test.go <inari-server>/e2e/
cd <inari-server> && make export-openapi
kubectl -n inari port-forward svc/inari-server 18090:8080 &
kubectl -n inari port-forward svc/keycloak-service 18091:8080 &
E2E_BASE_URL=http://127.0.0.1:18090 \
E2E_KEYCLOAK_URL=http://127.0.0.1:18091 \
E2E_OPENAPI=$PWD/dist/openapi.yaml \
go test -tags=e2e ./e2e/ -run TestAPISchemaConformance -count=1 -v
```

### Console UI e2e

Requires a running golden-path stack. Mirror the workflow step: port-forward
console/server/Keycloak (18080/18090/18091), run the persona seed (idempotent
— also whitelists the browser origin on the `inari-ui` Keycloak client),
point the console chart's `keycloakUrl` at the forward, start
`lib/ui-proxy.mjs`, then run the Playwright suite:

```sh
node scripts/e2e/ui/seed/seed-personas.mjs   # personas + inari-ui whitelist
node scripts/e2e/lib/ui-proxy.mjs &   # merges console + API onto :8080
cd scripts/e2e/ui
npm ci
npx playwright install --with-deps chromium
npx playwright test --config playwright.config.ci.ts --grep @p0 --grep-invert @quarantine
```

Failure artifacts (screenshots/videos/traces, JUnit, HTML report) land in
`scripts/e2e/ui/test-results/` and `scripts/e2e/ui/playwright-report/`. See
`ui/README.md` for the layout, spec-author rules, and topology constraints.

### kubectl access

Prereqs: `docker`, `kubectl`, `kubelogin`. Runs a control-plane-only
scenario (docker etcd + kube-apiserver + Keycloak) — no kind needed:

```sh
bash scripts/e2e/kubectl/kubectl-access.sh
```
