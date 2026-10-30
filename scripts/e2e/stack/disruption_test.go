//go:build e2e

package stack_test

import (
	. "github.com/onsi/ginkgo/v2"

	"7k-inari/inari-release-bundle/scripts/e2e/stack/suite"
)

// HA disruption phase (pod kill, rollout under traffic, migration-lock race,
// leader-lease single-execution, agent stream fencing, agent leader
// failover). Runs ONLY with E2E_HA=1, after the non-HA golden path has
// passed unchanged. Ordered: HA(d2) needs duplicate streams, which the
// leader election enabled in HA(d3) would suppress — sequence matters.
var _ = Describe("HA disruption", Label(suite.LabelP0), Label(suite.LabelDisruption), Ordered, func() {
	BeforeEach(func() {
		if !env.HA {
			Skip("E2E_HA != 1: HA disruption assertions run on the HA leg only")
		}
	})

	It("HA(c): fresh 0→2 scale-up serializes migrations (advisory lock)", func() {
		suite.HAMigrationLockRace(GinkgoT(), env)
	})
	It("HA(d1): leader-leased singleton loops run exactly once", func() {
		suite.HALeaseSingleExecution(GinkgoT(), env)
	})
	It("HA(d2): agent stream fencing evicts stale sessions", func() {
		suite.HAStreamFencing(GinkgoT(), env)
	})
	It("HA(d3): agent leader failover within budget", func() {
		suite.HAAgentLeaderFailover(GinkgoT(), env)
	})
	It("HA(a): pod kill under traffic", func() {
		suite.HAPodKill(GinkgoT(), env)
	})
	It("HA(b): rollout restart under traffic", func() {
		suite.HARolloutRestart(GinkgoT(), env)
	})
})
