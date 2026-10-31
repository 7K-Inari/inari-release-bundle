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
- Go stack suite phases map to surfaces implicitly by spec file
  (`tenant`, `cluster`, `rbac`, `policy`, `disruption`).

### 1.3 Execution-profile labels (phase 2)

| Label | Suite | Rule |
|---|---|---|
| `load` | k6 via k6-operator | **Never** in PR or release gates; weekly CronWorkflow only |
| `chaos` | LitmusChaos (runs as Argo Workflows) | **Never** in gates; dedicated env only, scheduled the night before the nightly-reset CronWorkflow |

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
| **Weekly** (CronWorkflow, `E2E_MODE=weekly`, phase 2) | `load \|\| chaos` | — | — | No |
| **PostSync hook** (ArgoCD PostSync after env sync) | `smoke` | `--grep @smoke` | `smoke` | Sync health only |

Notes:

- The gate **never** runs a quarantined test, in any suite. A quarantined
  test never blocks a release.
- HA is orthogonal to labels: `E2E_HA=1` adds the disruption legs as
  parallel DAG steps with their own `p0` specs.
- The api-schema conformance driver has no labels of its own; its ReportPortal
  launch carries `p0` (it is gate-only today).
- Docs/markdown-only changes on a Release PR intentionally skip the e2e gate
  (existing `gate` job detection) — unchanged.

---

## 4. Quarantine lifecycle

Flake containment pipeline (Playwright side exists today; the Ginkgo side
adopts it during the restructure):

1. **Detect.** The nightly runs the full surface including quarantined
   tests, with `continue-on-error`. CI Playwright runs use `retries: 2` — a
   test that passes only on retry is green in the gate but recorded as a
   flake candidate.
2. **File.** `ui/quarantine-issues.mjs` (nightly, stays in GHA — needs
   `issues:write`, no cluster) aggregates the last 7 days of flake reports;
   a test flaky in 2+ nightly runs gets an issue
   `[flake] <test path> — <test title>` labeled `e2e-flake` (dedupe by test
   path; an open issue gets a comment instead of a duplicate).
3. **Quarantine.** A human tags the test `quarantine` (and the issue
   number/reason in a comment). It immediately stops blocking gates.
4. **Expiry (new policy).** The quarantine-sweep gains the 14/30-day
   policy: a quarantined test with no fix activity for **14 days** gets a
   warning comment on its issue; at **30 days** the issue is escalated
   (the test is either fixed, re-scoped, or deleted — quarantine is a
   remediation queue, not a graveyard).
5. **Budget visibility.** The sweep maintains the **quarantined-test
   count** (Ginkgo + Playwright, counted from the specs) in a pinned
   tracking issue — the quarantine-budget signal without a metrics
   dashboard (Grafana is descoped).
6. **De-quarantine.** Remove the label and close the issue once the test
   has been stable in the nightly for ~2 weeks.

`ui/report-flakes.mjs` is retired once ReportPortal's analyzer covers flake
detection; `quarantine-issues.mjs` (the GitHub-issue pipeline) is **kept** —
ReportPortal visualizes, GitHub issues *act*.

---

## 5. Result contract (labels in reporting)

Every suite leg writes JUnit XML to a shared artifact directory:

```
/artifacts/{ginkgo,playwright,chainsaw,apischema,k6,scan}/junit.xml
```

- The workflow `onExit` handler uploads **all** JUnit to ReportPortal
  unconditionally — aborted and failed runs still report.
- Every launch carries attributes: `mode` (`E2E_MODE`), `E2E_RUN_ID`,
  `server-image`/`console-image` tags, `charts-ref`. ReportPortal's trend
  widgets + analyzer then slice history by mode/label.
- Playwright additionally writes its HTML report + traces (artifact only);
  live streaming via `@reportportal/agent-js-playwright` is optional.
- k6 emits JUnit via `handleSummary`; the Harbor scan gate emits a small
  synthetic JUnit (`image-scan: PASS, 0 critical, N high`).
- Artifacts live in a Garage S3 bucket (the platform cluster convention);
  `retention-days` honored via bucket lifecycle.

Labels must survive into JUnit: Ginkgo emits labels into the JUnit report
natively; Playwright tags are part of the test title (already true today —
the title is what `--grep` matches).

---

## 6. Current tag inventory (migration baseline)

Validated against the specs on 2026-10-30.

**Playwright** (`scripts/e2e/ui/specs/`):

| Spec | Tags | Notes |
|---|---|---|
| `smoke/access-tabs.spec.ts` | `@p0 @smoke` | the four RBAC tabs render their markers |
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
