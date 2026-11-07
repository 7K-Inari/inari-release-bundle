//go:build e2e

package stack_test

import (
	. "github.com/onsi/ginkgo/v2"

	"7k-inari/inari-release-bundle/scripts/e2e/stack/suite"
)

// RBAC materialization phase (plan §7.1): the state repo gains
// baseline/rbac/*, group membership maps to real RBAC, and a mapping flip
// converges (new binding, stale pruned). Ordered: each step builds on the
// previous one's state.
var _ = Describe("RBAC materialization", Label(suite.LabelP0), Label(suite.LabelRBAC), Ordered, func() {
	It("materializes baseline/rbac into the tenant state repo", func() {
		suite.WaitStateRepoMaterialized(GinkgoT(), env)
	})
	It("maps group membership to effective RBAC", func() {
		suite.SyncRBAC(GinkgoT(), env)
		suite.AssertBaselineRBACEffective(GinkgoT(), env)
	})
	It("converges a viewers→editor mapping flip (new binding, stale pruned)", func() {
		suite.FlipRBACMapping(GinkgoT(), env, "viewers", "editor")
		suite.SyncRBAC(GinkgoT(), env)
		suite.AssertMappingFlipConverged(GinkgoT(), env, "viewers", "editor")
	})
	// Self-restore rule (invariant 1, scripts/e2e/ui/README.md persona
	// discipline): the flip spec above leaves viewers→editor behind, which
	// poisons the persistent live env for the next run's baseline spec
	// (spec 2 expects viewers→viewer). DeferCleanup runs even on a
	// mid-container failure; the restore stays require-loud so a leaked
	// flip fails the run and forces human eyes.
	DeferCleanup(func() {
		suite.FlipRBACMapping(GinkgoT(), env, "viewers", "viewer")
		suite.SyncRBAC(GinkgoT(), env)
	})
})
