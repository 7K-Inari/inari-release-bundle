# scripts/e2e — end-to-end suite map

Canonical home of the Inari e2e suites (relocated from the inari-server
repo's `e2e/` directory). `.github/workflows/release-e2e.yaml` is the CI gate
that runs them; its golden-path legs and the nightly both delegate the full
stack leg (bring-up → follow-on suites → artifacts → teardown) to the
reusable `.github/workflows/e2e-stack.yaml` (`workflow_call`, migration
phase 4 — one shared bring-up definition, no YAML duplication).
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
├── kubectl/
│   ├── kubectl-access.sh         # control-plane-only kubectl access scenario
│   └── kubectl-tunnel.sh         # gateway-mode chain: kubeproxy → tunnel-agent → apiserver
├── lib/
│   └── ui-proxy.mjs              # single-origin shim shared by the UI suites
└── stack/                      # golden-path suite — provisions AND asserts (self-contained Go
    │                           #   module, tag e2e; shell→Go migration complete, golden-path.sh
    │                           #   deleted in phase 3)
    ├── main_test.go              # TestGoldenPath: in-process bring-up + ordered assertion phases
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
| Golden path (fast leg) | `stack/` (Go suite, `E2E_HA=0`) | `go test -tags=e2e -count=1 -timeout 40m -json ./...` from `scripts/e2e/stack/` (nested module — won't resolve from the repo root): provisions the whole kind stack in-process, then asserts. Quarantined Go tests skip (no `-include-quarantined`). | `release-e2e.yaml` job `golden-path` (matrix `ha: false`) → reusable `e2e-stack.yaml` (`mode: gate`) on Release PRs | Yes — Release-PR gate |
| Golden path (HA leg) | `stack/` (Go suite, `E2E_HA=1` flips helm replica values + unlocks disruption subtests) | same, with `E2E_HA=1` + `SERVER_MIGRATIONS_DIR` | `release-e2e.yaml` job `golden-path` (matrix `ha: true`) → `e2e-stack.yaml` (`mode: gate`) on Release PRs | Yes — Release-PR gate |
| API schema conformance | `api/api_schema_e2e_test.go` | `go test -tags=e2e` inside the inari-server checkout at the pinned tag | `e2e-stack.yaml` step "Run api-schema e2e against the live stack" (after the stack suite, `mode: gate` only) | Yes — Release-PR gate |
| Console UI e2e | `ui/` (Playwright Test) + `lib/ui-proxy.mjs` + `ui/seed/seed-personas.mjs` | `npx playwright test --config playwright.config.ci.ts --grep @p0 --grep-invert @quarantine` in `ui/` | `e2e-stack.yaml` step "Run console UI e2e (Playwright)" on BOTH gate HA matrix legs (after api-schema) | Yes — Release-PR gate |
| Golden path (nightly, incl. quarantined Go tests) | `stack/` (Go suite) | same as the fast leg, plus `-args -include-quarantined` so `suite.Quarantined` tests run | `e2e-nightly.yaml` job `nightly-stack` → `e2e-stack.yaml` (`mode: nightly`, cron 03:17 UTC + `workflow_dispatch`) | No — nightly only, non-blocking |
| Console UI e2e (full, incl. quarantine) | `ui/` (Playwright Test) | `npx playwright test --config playwright.config.ci.ts --grep "@p0\|@p1\|@p2"` (includes `@quarantine`, `continue-on-error`) | `e2e-nightly.yaml` job `nightly-stack` → `e2e-stack.yaml` (`mode: nightly`) — same single bring-up as the Go suite | No — nightly only, non-blocking |
| kubectl access | `kubectl/kubectl-access.sh` | bash script (docker etcd + kube-apiserver; no kind) | not wired into CI yet | No — manual scenario |
| kubectl tunnel (gateway) | `kubectl/kubectl-tunnel.sh` | bash script (docker etcd + kube-apiserver + Keycloak + postgres + OpenFGA + kubeproxy + tunnel-agent; no kind). Builds `inari/kubeproxy:e2e` and `inari/agent:e2e` from sibling checkouts (`INARI_SERVER_DIR`/`INARI_AGENT_DIR`); asserts impersonation, 503 upgrade path, 410 kill-switch, per-cluster client revoke, max-lifetime reaper | `e2e-nightly.yaml` job `kubectl-tunnel` (builds inari-server at the resolved tag + inari-agent main) | No — nightly only, non-blocking |

## Workflow call graph

```
release-e2e.yaml  (Release-PR gate: gate → resolve → server-integration)
└── golden-path (matrix ha:[false,true])  ──uses──▶ e2e-stack.yaml (mode=gate, 7d artifacts)

e2e-nightly.yaml  (cron + dispatch, non-blocking)
├── resolve (appVersion → server tag)
├── nightly-stack                       ──uses──▶ e2e-stack.yaml (mode=nightly, 14d artifacts)
└── quarantine-sweep (needs: nightly-stack; issues:write; no cluster needed)
```

Artifact retention: gate UI artifacts `ui-e2e-ha-{false,true}` (failure-only,
compression-level 0) and flake reports `ui-e2e-flakes-ha-{false,true}` 7 days;
Go test JSON + cluster diagnostics `golden-path-go-ha-{false,true}`
failure-only 7 days; nightly artifacts (`ui-e2e-nightly`,
`ui-e2e-flakes-nightly`, `golden-path-go-nightly`) 14 days.

## Flakiness containment

The gate NEVER runs a quarantined test — the direct analog of inari-server's
`flaky` build-tag convention.

- **Gate command in effect** (release-e2e.yaml, both HA legs):
  `npx playwright test --config playwright.config.ci.ts --grep @p0 --grep-invert @quarantine`.
  CI runs with `retries: 2`, so a test that passes only after a retry is
  green in the gate but recorded as a flake candidate.
- **Flake reporter** (`ui/report-flakes.mjs`): post-processes the Playwright
  JSON report after every CI run (gate + nightly), logs a summary, and
  writes `test-results/flake-report.json` (test title, file, run id,
  timestamp, branch). Artifacts: `ui-e2e-flakes-ha-<bool>` on gate runs
  (7-day retention), `ui-e2e-flakes-nightly` on nightly runs (14-day
  retention, plus full Playwright HTML/JUnit results).
- **Quarantine policy** (manual part): when an `e2e-flake` issue is triaged,
  tag the test `@quarantine` in its spec title. Quarantined tests are
  excluded from the gate (`--grep-invert @quarantine`) and run only in the
  nightly. De-quarantine (remove the tag, close the issue) once the test
  has been stable in the nightly for ~2 weeks.
- **Quarantined Go tests** (stack suite): call `suite.Quarantined(t,
  "flake: <reason>, issue #NNN")` as the first statement of the flaky
  test/subtest body (`stack/suite/quarantine.go`). It skips unless the suite
  runs with `-args -include-quarantined` — the runtime-flag analog of
  inari-server's `flaky` build tag. The gate NEVER sets the flag; the
  nightly (`mode: nightly`) always does. De-quarantine by removing the
  `Quarantined` call once stable in the nightly for ~2 weeks.
- **Quarantine automation** (`ui/quarantine-issues.mjs`, nightly only):
  aggregates the last 7 days of `ui-e2e-flakes-*` artifacts; a test flaky in
  2+ nightly runs within the window gets an issue titled
  `[flake] <test path> — <test title>` labeled `e2e-flake`. Dedupe is by
  test path: an open issue for the same path gets a comment with the new
  occurrences instead of a duplicate.

## Running locally

### Golden path (kind full stack)

Prereqs: `docker`, `kind`, `kubectl`, `helm`, `jq`, `git`, Go, plus checkouts
of the `inari-agent` and `inari-operator` repos next to this one (or set
`AGENT_CHART_DIR` / `OPERATOR_CHART_DIR` explicitly). Build the e2e images
first (`inari/server:e2e` from inari-server, `inari/agent:e2e` from
inari-agent). For the HA assertions also set `SERVER_MIGRATIONS_DIR` to the
inari-server checkout's `internal/db/migrations`.

One command (shell→Go migration phase 3 — `golden-path.sh` is deleted):
the Go stack suite provisions the whole stack in-process (kind cluster +
git hostPath → operators → helm charts in dependency layers → KC seeding →
tenant/cluster/agent) and then asserts:

```sh
cd scripts/e2e/stack
KEEP_CLUSTER=true E2E_HANDOFF_PATH=/tmp/inari-e2e-handoff.json \
  go test -tags=e2e -count=1 -timeout 40m ./...    # add E2E_HA=1 + SERVER_MIGRATIONS_DIR for the HA leg
```

`KEEP_CLUSTER=true` keeps the kind cluster and the git host dir alive after
the run (for the api-schema/UI suites or inspection); without it the suite
tears both down at test end. Pointing `E2E_HANDOFF_PATH` at an EXISTING
handoff file skips provisioning entirely and asserts against that
already-running stack (fast local reruns). `-timeout` must exceed go
test's 10m default — bring-up + assertions take longer.

Env knobs (defaults in `stack/suite/provision.go`): `CLUSTER_NAME`,
`NAMESPACE`, `TENANT`, `SERVER_IMAGE` / `AGENT_IMAGE`, `HELM_CHARTS_DIR`
plus the per-chart `*_CHART_DIR` overrides, `VAULT_DEV_TOKEN`,
`INARI_E2E_CACHE_BACKEND` (memory|redis; redis default on the HA leg),
`KIND_NODE_IMAGE` (default: pinned `kindest/node` digest from
`stack/testdata/images.txt`; empty = kind's built-in default),
`E2E_ADOPT_CLUSTER=1` + `GIT_HOST_DIR` (reuse a pre-created cluster and a
fixed git root — CI's image-preload path; locals leave both unset),
`HELM_REPOS_SEEDED=1` (skip helm repo add/update — CI sets it on a helm
cache hit; the restored `~/.config/helm` + `~/.cache/helm` already carry
the repo config and indexes),
`E2E_PROVISION_ONLY=1` (bring-up without assertions — local use only; CI
no longer uses it: the nightly asserts too, incl. quarantined tests via
`-args -include-quarantined`).

## CI caching

The reusable `e2e-stack.yaml` workflow caches everything the stack leg
pulls repeatedly. All caches are keyed on exact pinned versions — a bump
MUST miss the cache; mutable tags are never cached.

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
cosign-verified per job), `inari/agent:e2e` (built in-job — the buildx GHA
cache covers it), and `ghcr.io/7k-inari/inari-operator:*` (private,
dynamic appVersion tag).

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
