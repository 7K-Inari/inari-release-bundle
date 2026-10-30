//go:build e2e

package stack_test

import (
	. "github.com/onsi/ginkgo/v2"

	"7k-inari/inari-release-bundle/scripts/e2e/stack/suite"
)

// Tenant phase: platform group sync → org_creator tuple, /me/permissions,
// creator auto-membership via outbox, outbox drain through the NATS relay,
// cache-layer metrics.
var _ = Describe("Tenant", Label(suite.LabelP0), Label(suite.LabelTenants), func() {
	It("HA(c) companion: converges on a single OpenFGA store", func() {
		if !env.HA {
			Skip("E2E_HA != 1: single-store assertion runs on the HA leg only")
		}
		suite.AssertFGASingleStore(GinkgoT(), env)
	})
	It("syncs the org_creator tuple to OpenFGA and /me/permissions", func() {
		suite.AssertOrgCreatorSync(GinkgoT(), env)
	})
	It("materializes creator tenant membership via the outbox", func() {
		suite.AssertTenantMembership(GinkgoT(), env)
	})
	It("drains the outbox through the NATS relay", func() {
		suite.AssertOutboxDrained(GinkgoT(), env)
	})
	It("exposes the cache-layer metrics series", func() {
		suite.AssertCacheMetrics(GinkgoT(), env)
	})
})
