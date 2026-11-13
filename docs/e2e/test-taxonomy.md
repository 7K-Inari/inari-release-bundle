# E2E test taxonomy

**Status:** normative target for the e2e migration (approved architecture plan
§3.3). One label taxonomy across every suite — Ginkgo, Playwright, Chainsaw —
so a stage (release gate, nightly, weekly, PostSync smoke) selects tests by
the same vocabulary everywhere. Companion document:
[environment-contract.md](environment-contract.md).

---

## 1. Label classes

### 1.1 Priority labels (required on every test)

| Label | Meaning | Gate consequence |
|---|---|---|
| `p0` | Release-critical path. Breakage means the candidate artifact set must not ship. | Runs in the release gate; failure blocks the Release PR |
| `p1` | Core feature behavior; important but with a tolerable workaround or narrow blast radius. | Nightly only; failure files an issue, does not block a release |
| `p2` | Extended/edge coverage. | Nightly only, same as `p1` |

Every test carries **exactly one** priority label. A test without a priority
label never runs anywhere — the stage filters (§3) select by priority, so an
unlabeled test is dead code. Suites should treat "no priority label" as a
lint error. The one deliberate exception is the manual-only `renderpath`
label (§1.5).

### 1.2 Surface labels (optional, additive)

Describe *what area* a test covers; used for ad-hoc selection and flake
triage, never for gating decisions on their own:

- `smoke` — minimal "is the stack alive" set. Also the PostSync-hook set.
- `rbac`, `settings`, `tenants` — UI surface areas (mirrors the
  `specs/<area>/` directory layout).
- `visual`, `a11y` — Playwright-only visual-regression and axe accessibility
  quality signals (W8). Every spec also carries `@p2`, so these labels select
  ad-hoc runs but never alter gating: both layers are nightly-only.
- `security` — the Harbor/Trivy image scan gate: a workflow step on the
  `e2e-stack` template (release mode only), not a Ginkgo/Playwright label —
  it surfaces as the `security` JUnit testsuite in the run's ReportPortal
  launch.
- Go stack suite phases map to surfaces implicitly by spec file
  (`tenant`, `cluster`, `rbac`, `policy`, `disruption`).

### 1.3 Execution-profile labels (phase 2)

| Label | Suite | Rule |
|---|---|---|
| `load` | k6 via k6-operator TestRuns — realized (W7) as the sequential `k6-run` legs of the e2e-stack template (`mode=weekly`; `scripts/e2e/load/`), not a Ginkgo/Playwright label | **Never** in PR or release gates; weekly CronWorkflow only |
| `fuzz` | Schemathesis via the `fuzz` leg of the e2e-stack template (`mode=nightly`; `scripts/e2e/fuzz/`), not a Ginkgo/Playwright label | **Never** in PR or release gates; nightly only, serialized after Ginkgo and Playwright; surfaces as the `fuzz` JUnit testsuite |
| `chaos` | LitmusChaos, realized (W9) as five sequential `chaos-run` legs of the e2e-stack template (`mode=weekly`, `weekly-suite=chaos|all`; `scripts/e2e/chaos/`), not a Ginkgo/Playwright label | **Never** in PR or release gates; dedicated env only. Manual chaos-only acceptance is supported. Recurring `weekly-suite=all` stays gated on three green scheduled nightlies + green weekly k6 and an enabled, restore-tested nightly reset; the cron remains `load` until then |

The kubeproxy/tunnel throughput target from the migration plan is
**deliberately dropped** (W7 D4): kubeproxy is disabled on the e2e env and
the tunnel chain stays docker-only — there is nothing on the env to load.
Revisit only if the tunnel is ever enabled there.

### 1.4 Lifecycle label

| Label | Meaning |
|---|---|
| `quarantine` | Known-flaky test under remediation. **Never runs in any gate**; runs in the nightly (with `include-quarantined` semantics). See §4. |

### 1.5 Manual-only label

| Label | Meaning |
|---|---|
| `renderpath` | Ginkgo `Label("renderpath")` (`stack/suite/labels.go` `LabelRenderPath`). The deliberate exception to the "exactly one priority label per spec" rule: the render-path validation flow mutates the stack **destructively** (render cutover + RGD teardown), so it must **never** be selected by a stage filter — neither the gate's `(p0 \|\| smoke) && !quarantine` nor the nightly's `p0 \|\| p1 \|\| p2` matches it. Runs manual-only via an explicit `--ginkgo.label-filter=renderpath` (the Ginkgo port of the old `-run TestRenderPath` targeted invocation). 13 specs in `scripts/e2e/stack/renderpath_test.go`. |

---

## 2. One mechanism per runner, one vocabulary overall

| Runner | Mechanism | Example |
|---|---|---|
| Go stack suite (Ginkgo v2 + Gomega) | `Label(...)` on specs, selected with `--label-filter` | `It("creates a tenant", Label("p0"), Label("tenants"), ...)` |
| Playwright UI suite | `@tag` in the test/describe title, selected with `--grep` / `--grep-invert` | `test("... @p0 @smoke", ...)` |
| Chainsaw | labels in test metadata, selected by workflow step (not by filter expression) | `smoke` set for PostSync hooks |

The Ginkgo `Label("p0")` ↔ Playwright `@p0` mapping is **1:1 by name** —
renaming a label is a cross-suite change. Chainsaw tests carry the same
label names in metadata.

### Quarantine mechanism migration (done)

The two suites quarantined differently before the taxonomy; they are now
unified:

- **Playwright:** `@quarantine` title tag — **unchanged**.
- **Go stack suite:** `suite.Quarantined(t, reason)` +
  `-args -include-quarantined` (`stack/suite/quarantine.go` +
  `internal/quarflag`) was **retired** in the Ginkgo restructure (PR #106) —
  replaced by `Label("quarantine")` and `--label-filter`. The gate never
  selects the label; the nightly always does. This is the same hard rule as
  inari-server's `flaky` build tag (which is unchanged — it is repo-native
  unit/integration gating, not part of this taxonomy).
- **inari-server `flaky` build tag:** unchanged, out of scope here.

---

## 3. Stage → selection matrix

| Stage | Ginkgo filter | Playwright filter | Chainsaw | Gating |
|---|---|---|---|---|
| **Release gate** (`E2E_MODE=release`, via thin `release-e2e.yaml` → Argo) | `(p0 \|\| smoke) && !quarantine` | `--grep "@p0\|@smoke" --grep-invert @quarantine` | `smoke` set only | **Yes** — blocks the Release PR |
| **Nightly** (CronWorkflow, `E2E_MODE=nightly`) | `p0 \|\| p1 \|\| p2` incl. `quarantine` | `--grep "@p0\|@p1\|@p2"` incl. `@quarantine` (`continue-on-error`) | all | No — signal + flake sweep |
| **Upgrade** (`E2E_MODE=upgrade`, phase 2 — W5) | `(p0 \|\| smoke) && !quarantine` (the gate selection, re-run on the upgraded stack) | `--grep "@p0\|@smoke" --grep-invert @quarantine` | full bootstrap set, post-upgrade | **No — non-gating** (suspended CronWorkflow + manual); release-gating flip only after ~2 weeks stable, see below |
| **Weekly** (CronWorkflow, `E2E_MODE=weekly`, phase 2) | — (W7 k6 + W9 Litmus are workflow legs, not suite labels. k6 runs for `weekly-suite=load|all`; five sequential chaos scenarios run for `chaos|all`, after successful/skipped k6. The default/cron remains `load` until the W9 activation gates.) | Chaos baseline/recovery `@smoke`; during-fault `@p0\|@smoke --grep-invert @quarantine` (NATS `@smoke` only). No standalone weekly Playwright shard | — | No |
| **PostSync hook** (ArgoCD PostSync after env sync) | `smoke` | `--grep @smoke` | `smoke` | Sync health only |

Notes:

- The gate **never** runs a quarantined test, in any suite. A quarantined
  test never blocks a release.
- HA is orthogonal to labels: `E2E_HA=1` adds the disruption legs as
  parallel DAG steps with their own `p0` specs.
- The api-schema conformance driver has no labels of its own; its ReportPortal
  launch carries `p0` (it is gate-only today).
- `@p2 @visual` and `@p2 @a11y` join the existing two Playwright shards in the
  nightly. The priority filters exclude them from release, edge, upgrade, and
  smoke runs; they have no separate shards, workflow legs, or gates.
- `fuzz` is a nightly workflow leg, not a per-test label. Its `when` clause
  excludes every other mode, and its dependency expression runs it after both
  Ginkgo and Playwright regardless of their outcomes so a red suite does not
  starve the fuzz signal.
- `chaos` is a weekly workflow execution profile, not a per-test label.
  `weekly-suite=chaos` selects focused manual acceptance; `all` runs k6 then
  CNPG failover → Keycloak latency → NATS outage → OpenFGA latency/timeout →
  server replacement. `chaos-scenario` selects one named scenario or `all`;
  `negative-control` is deliberately excluded from `all` and fails its manual
  Argo leg after proving the outage and recovering. Each scenario emits a
  `chaos.<scenario>` JUnit wrapper plus raw ChaosResult/evidence. Recurring
  activation and reset/stability evidence are documented in
  `scripts/e2e/chaos/README.md`.
- **Upgrade is a stage, not a per-test label.** The `upgrade` stage
  (phase 2, W5) is realized as `E2E_MODE=upgrade` on the e2e-stack
  WorkflowTemplate: the run deploys the previous stable artifact set, writes
  a pre-upgrade fixture tenant, upgrades to the candidate artifacts, then
  re-selects the gate set above — no spec carries an `upgrade` label. The
  leg adds its own synthetic `upgrade` JUnit testsuite (fixture-survival
  assertion). Non-gating by construction; the release-gating flip is a
  second `argo submit` in `release-e2e.yaml`, a deliberate follow-up after
  ~2 weeks of stable runs (drill + unsuspension criteria:
  `gitops/e2e/README.md` §upgrade leg).
- Docs/markdown-only changes on a Release PR intentionally skip the e2e gate
  (existing `gate` job detection) — unchanged.

---

## 4. Quarantine lifecycle

Flake containment pipeline (one mechanism across Ginkgo, Playwright, and
Chainsaw — all suites land in the same ReportPortal launch):

1. **Detect.** The nightly CronWorkflow runs the full surface including
   quarantined tests. CI Playwright runs use `retries: 2`, so a test that
   passes only on a retry imports into ReportPortal as mixed FAILED+PASSED
   items under one launch (skipped testcases are dropped at import, so item
   statuses are clean signal); a test failing outright imports all-FAILED.
2. **File.** `ui/quarantine-issues.mjs` (nightly, stays in GHA — needs
   `issues:write`, no cluster) queries ReportPortal for the last 7 days of
   nightly launches (launch name `nightly-*`, attribute `mode:nightly`) and
   groups items by stable test identity (pathNames + name; the RP
   `uniqueId` is launch-specific). A test unstable in 2+ nightly launches
   gets an issue `[flake]|[failure] <test path> — <test title>` labeled
   `e2e-flake` (dedupe by test identity; an open issue gets a comment
   instead of a duplicate). The workflow's synthetic anchors
   (`e2e.run`/`workflow-outcome`, `upgrade`) and already-quarantined tests
   are excluded. Auth: the `REPORTPORTAL_API_KEY` repo secret (read-only RP
   user `e2e-sweeper`, project role CUSTOMER — owner action, see
   `gitops/e2e/README.md` §Prerequisites; the importer key stays
   cluster-only). Missing key/unreachable RP ⇒ the sweep skips with a
   warning and stays exit-0.
3. **Quarantine.** A human tags the test `quarantine` (and the issue
   number/reason in a comment). It immediately stops blocking gates.
4. **Expiry.** Implemented in the sweep: an open `e2e-flake` issue with no
   **human** activity for **14 days** gets one warning comment (marker
   `quarantine-sweep:warn-14d`); at **30 days** one escalation comment
   (marker `quarantine-sweep:escalate-30d`) plus the `quarantine-overdue`
   label — the test must then be fixed, re-scoped, or deleted. Bot/sweep
   comments never reset the clock; any human comment does. The sweep never
   closes issues and never deletes tests — quarantine is a remediation
   queue, not a graveyard, and enforcement is human.
5. **Budget visibility.** Implemented in the sweep: the **quarantined-test
   count** (Playwright `@quarantine` tags + Ginkgo `LabelQuarantine`
   labels, counted from the specs) is maintained in a pinned
   `[quarantine-budget]` tracking issue; over `QUARANTINE_BUDGET` (default
   10) the body flags it and the sweep comments. This is the
   quarantine-budget signal without a metrics dashboard (Grafana is
   descoped).
6. **De-quarantine.** Remove the label and close the issue once the test
   has been stable in the nightly for ~2 weeks.

`ui/report-flakes.mjs` is **retired** (deleted; ReportPortal history
replaces it); `quarantine-issues.mjs` (the GitHub-issue pipeline) is
**kept** — ReportPortal visualizes, GitHub issues *act*.

---

## 5. Result contract (labels in reporting)

Every suite leg writes JUnit XML to a shared artifact directory:

```
/artifacts/{ginkgo,playwright,chainsaw,apischema,k6,scan,fuzz}/junit.xml
/artifacts/out/junit.xml                    # one chaos-run leg; S3 key includes chaos/<scenario>/
```

- The workflow `onExit` handler uploads **all** JUnit to ReportPortal
  unconditionally — aborted and failed runs still report.
- Every launch carries attributes: `mode` (`E2E_MODE`), `E2E_RUN_ID`,
  `server-image`/`console-image` tags, `charts-ref`, plus workflow-only
  `weekly-suite`/`chaos-scenario` selectors. ReportPortal's trend
  widgets + analyzer then slice history by mode/label.
- Playwright additionally writes its HTML report + traces (artifact only);
  live streaming via `@reportportal/agent-js-playwright` is optional.
- Schemathesis emits JUnit directly (W8), normalized to the `fuzz` testsuite;
  its full JUnit, VCR cassette, JSON summary, and crash/replay files are archived
  at `e2e/<uid>/fuzz/`. The exit-handler's optional `fuzz-junit` input joins the
  same nightly ReportPortal launch.
- k6 emits JUnit via `handleSummary` + the vendored k6-summary `jUnit()`
  (realized in W7: one plain pass/fail testcase per threshold, classname
  `k6.<scenario>`, artifact keys `e2e/<uid>/k6/<scenario>/junit.xml`); the
  Harbor scan gate emits a small
  synthetic JUnit (testsuite `security`, one `image-scan <repo>:<tag>`
  testcase per candidate image; failures carry the Critical count, the
  threshold, and the S3 pointer to the full `additions/vulnerabilities`
  report archived at `e2e/<uid>/scan/reports/`).
- Chaos emits one `chaos.<scenario>` synthetic JUnit per selected leg at
  `e2e/<uid>/chaos/<scenario>/junit.xml`, with raw results and browser/domain
  evidence under the sibling `evidence/` key. Native phase/verdict and the
  unchanged Litmus `probeSuccessPercentage` are separate testcases from
  Playwright, domain, cleanup, and recovery verdicts, so a native 100% cannot
  mask an independent failure. The optional negative-control wrapper is failed
  by design and still imports through the unconditional exit handler.
- Artifacts live in a Garage S3 bucket (the platform cluster convention);
  `retention-days` honored via bucket lifecycle.
- Skipped testcases are dropped from the ReportPortal **import copy** and
  never appear in RP (the JUnit plugin would otherwise derive FAILED for any
  suite containing a skipped descendant); the full JUnit, skips included,
  remains in the S3 artifact (PR #147).

Labels must survive into JUnit: Ginkgo emits labels into the JUnit report
natively; Playwright tags are part of the test title (already true today —
the title is what `--grep` matches).

---

## 6. Current tag inventory (migration baseline)

Validated against the specs on 2026-11-12.

**Playwright** (`scripts/e2e/ui/specs/`):

| Spec | Tags | Notes |
|---|---|---|
| `smoke/access-tabs.spec.ts` | `@p0 @smoke` | the four RBAC tabs render their markers |
| `visual/key-pages.spec.ts` | `@p2 @visual` | four container-locked live-page screenshot baselines; nightly-only |
| `a11y/key-pages.spec.ts` | `@p2 @a11y` | axe WCAG 2.0/2.1 A+AA scans of the app shell, dashboard, all Access tabs, and org profile; nightly-only |
| `rbac/role-lifecycle.spec.ts` | `@p0 @rbac` (+ 3 `@quarantine` tests) | mutates `e2e-member`, must restore it role-less |
| `rbac/builtin-protection.spec.ts` | `@p0 @rbac` | built-in role 409s, tenant.admin guardrail |
| `rbac/role-gating-matrix.spec.ts` | `@p1 @rbac` | per-persona gating matrix |
| `settings/org-profile.spec.ts` | `@p1 @settings` | display-name write path + audit |
| `settings/git-config.spec.ts` | `@p1 @settings` | git write path via local provider (→ `E2E_GIT_URL`) |
| `tenants/onboarding.spec.ts` | `@p1 @tenants` | org creation; intentionally leaks the org |
| `tenants/switching.spec.ts` | `@p1 @tenants` | tenant switching; intentionally leaks the org |

**Go stack suite** (`scripts/e2e/stack/`): Ginkgo v2 labels per spec —
priority (`p0`/`p1`/`p2`), `smoke`, surface labels (`tenants`, `cluster`,
`rbac`, `policy`, `disruption`), and `quarantine`, all defined in
`suite/labels.go`; phases map to spec files (`tenant_test.go`,
`cluster_test.go`, …, `disruption` specs gated on `E2E_HA=1`). The
destructive render-path flow (`renderpath_test.go`) carries the manual-only
`Label("renderpath")` (§1.5) instead of a priority label. The Ginkgo
restructure (phase P1b, PR #106) assigned the labels; behavior was
preserved, only structure changed.

**Chainsaw** (`scripts/e2e/chainsaw/`, phase P1a): new — declarative
cluster-state assertions replacing `scripts/lib/verify.sh` + bootstrap/DR
smoke checks; carries the same labels via metadata; also runs as ArgoCD
PostSync hooks.
