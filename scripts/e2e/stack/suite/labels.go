// Canonical test labels for the stack suite — the Go side of the single
// cross-suite taxonomy (docs/e2e/test-taxonomy.md). Ginkgo Label("p0") maps
// 1:1 to the Playwright "@p0" tag; renaming a label is a cross-suite change.
//
// Selection happens via --ginkgo.label-filter:
//
//	release gate:  (p0||smoke)&&!quarantine
//	nightly:       p0||p1||p2            (includes quarantine-labeled specs)
//	PostSync hook: smoke
//
// This file intentionally carries NO e2e build tag and does not import
// Ginkgo: plain string constants keep package suite buildable without the
// tag (so `go build ./...` / `go vet ./...` on the repo keep working) and let
// the taxonomy be referenced from tooling that never compiles the suite.
package suite

const (
	// Priority labels — exactly one per spec. A spec without a priority
	// label is never selected by any stage filter (dead code).
	LabelP0 = "p0" // release-critical; failure blocks the Release PR
	LabelP1 = "p1" // core behavior, tolerable workaround; nightly only
	LabelP2 = "p2" // extended/edge coverage; nightly only

	// LabelSmoke is the minimal "is the stack alive" set — also the
	// ArgoCD PostSync hook set.
	LabelSmoke = "smoke"

	// LabelQuarantine marks a known-flaky spec under remediation. Gate
	// filters always invert it; the nightly includes it. This replaces the
	// retired suite.Quarantined + -args -include-quarantined mechanism.
	LabelQuarantine = "quarantine"

	// Surface labels (optional, additive) — what area a spec covers; used
	// for ad-hoc selection and flake triage, never for gating.
	LabelTenants    = "tenants"
	LabelCluster    = "cluster"
	LabelRBAC       = "rbac"
	LabelPolicy     = "policy"
	LabelDisruption = "disruption"
)
