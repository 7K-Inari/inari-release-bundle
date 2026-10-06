# inari-release-bundle — Agent Guide

Release bundle for Inari: the core Helm charts (charts/inari-platform, charts/inari-server, charts/inari-console), ArgoCD Application definitions (gitops/), day-0 bootstrap (plan §6 #10, §9 M0). Formerly inari-helm-charts.

Stack: ArgoCD sync waves over Helm charts (chart releases as OCI)

## Key architecture constraints
- **Day-0 bootstrap: Inari must never require Inari to install** — scripted first-platform-cluster install lives here (§12.1/1).
- gitops/ composes: Keycloak (realm `inari`), PostgreSQL (CNPG), NATS, OpenFGA via ArgoCD Applications; inari-server/inari-console charts release from this repo; the inari-operator/inari-agent charts stay in their component repos. Everything publishes to the org-level namespace `oci://ghcr.io/7k-inari/charts` (§4.2).
- Backup/restore runbook coverage: PostgreSQL, OpenFGA store, Keycloak config, NATS — a tested restore is an M0 exit criterion (§9 M0, §12.1/1).
- `scripts/e2e/` owns the e2e assets (relocated from inari-server), organized per surface: `api/api_schema_e2e_test.go` (Go driver), `kubectl/kubectl-access.sh` (direct-access chain), `kubectl/kubectl-tunnel.sh` (gateway chain: kubeproxy + tunnel-agent + real apiserver over docker, builds sibling inari-server/inari-agent checkouts via `INARI_SERVER_DIR`/`INARI_AGENT_DIR`; runs non-blocking in the `e2e-nightly.yaml` `kubectl-tunnel` job), `lib/ui-proxy.mjs` (shared UI shim), `stack/` (golden-path Go suite — self-contained module, `go test -tags=e2e`; provisions the whole kind stack in-process AND asserts it: kind/hostPath/operators in `suite/provide.go`, dependency-layered concurrent helm installs in `suite/provision.go`, KC seeding + tenant/cluster/agent in `suite/seed.go`, assertion phases incl. HA disruption gated on `E2E_HA=1`, thin CLI-exec wrappers in `internal/`, e2e values trims in `testdata/`; `E2E_HANDOFF_PATH` at an existing file attaches assert-only. golden-path.sh was deleted in shell→Go migration phase 3) and `ui/` (Playwright Test suite — setup-project auth personas, page objects, `@p0 @smoke` access-tabs spec; personas + the inari-ui redirect whitelist are seeded by `ui/seed/seed-personas.mjs`; see `scripts/e2e/ui/README.md`). `scripts/e2e/README.md` is the suite map (incl. the flakiness-containment policy: gate runs `--grep @p0 --grep-invert @quarantine`, `ui/report-flakes.mjs` + `ui/quarantine-issues.mjs`, and the non-blocking `.github/workflows/e2e-nightly.yaml` runs the full suite incl. `@quarantine`). The reusable `.github/workflows/e2e-stack.yaml` (`workflow_call`, migration phase 4) owns the whole stack leg — bring-up, follow-on suites, artifacts, teardown — and is called by both `release-e2e.yaml`'s golden-path job (`mode: gate`, HA matrix) and the nightly (`mode: nightly`); Go-side quarantine is `suite.Quarantined(t, reason)` in `stack/suite/quarantine.go`, skipped unless `-args -include-quarantined` (nightly only — the gate never sets it, same rule as inari-server's `flaky` build tag). `.github/workflows/release-e2e.yaml` gates Release PRs with them; the workflow still checks out inari-server at the pinned tag for the Go integration/api-schema suites. Never reintroduce e2e assets into inari-server.
- Charts published as OCI artifacts; lint + template tests in CI (chart-testing).

## Conventions
- Conventional Commits; SemVer releases; container images/artifacts cosign-signed (once CI exists).
- Releases: release-please in PR-only mode (manifest mode, one component per chart path). Pushes to `main` open/update a Release PR with `Chart.yaml` bumps (via `x-release-please-version` annotations) and per-chart CHANGELOGs. Merging the Release PR triggers `release.yaml`, which creates per-chart tags (`<chart>-vX.Y.Z`, e.g. `inari-platform-v0.4.1`), GitHub Releases, and publishes charts to `oci://ghcr.io/7k-inari/charts`. Charts version independently — never bump `version:`/`appVersion:` in `Chart.yaml` by hand.
- `charts/inari-server`/`charts/inari-console` `appVersion:` (and inari-console's `values.yaml` `bundle.tag`) are owned by the cross-repo sync: component release pipelines dispatch `appversion-bump` to `.github/workflows/chart-sync.yml`, which commits `fix(<chart>): bump appVersion to vX.Y.Z` so the chart releases in lockstep. Never hand-edit those fields; never annotate them `x-release-please-version`.
- The platform-config chart was renamed `inari-platform` (ArgoCD Application/release keep the legacy name `platform-config`). Its `agent.supportedRange`/`agent.recommended` values declare the supported inari-agent versions (ConfigMap `inari-agent-compat`) — human-maintained via PR.
- Edge releases: every merge to `main` runs `.github/workflows/edge.yml`, which publishes each chart at `<pending-version>-<shortsha>` (pending version resolved by `scripts/resolve-edge-version.sh` from the open Release PR's manifest, fallback manifest+patch) and cuts per-chart GitHub prereleases plus the moving `edge` channel tag/release. Edge runs skip release merges.
- Write tests for new behavior; keep changes minimal and focused.
- Canonical architecture & development plan: https://github.com/7K-Inari/inari-docs/blob/main/docs/architecture/inari-platform-plan.md (section references below point into it).

## Platform design principles (apply everywhere)
1. Tenant-aware to the core — every object carries a tenant ID; every API decision is tenant-scoped.
2. Zero tenant credentials on the hub — no tenant kubeconfigs or cloud keys in the control plane.
3. Pull, never push — agents dial out; the control plane never initiates connections into tenant networks.
4. Desired state, eventually reconciled — GitOps/CR-based mutations, not imperative RPCs.
5. The catalog is a projection of reality — capabilities are discovered, not declared.
6. Small kernel, everything else extension.
7. Modular monolith first — strict internal module boundaries.
