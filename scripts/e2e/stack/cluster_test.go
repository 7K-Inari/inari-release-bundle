//go:build e2e

package stack_test

import (
	. "github.com/onsi/ginkgo/v2"

	"7k-inari/inari-release-bundle/scripts/e2e/stack/suite"
)

// Cluster phase: agent connect (cluster state active), ESO-projected OIDC
// client secret, capability stream, heartbeat freshness. "Cluster active" is
// the suite's smoke check — the minimal proof the stack is alive end-to-end.
var _ = Describe("Cluster", Label(suite.LabelP0), Label(suite.LabelCluster), func() {
	It("reports the cluster active once the agent connects", Label(suite.LabelSmoke), func() {
		suite.AssertClusterActive(GinkgoT(), env)
	})
	It("projects the agent OIDC client secret via ESO", func() {
		suite.AssertESOAgentClientSecret(GinkgoT(), env)
	})
	It("streams capabilities", func() {
		suite.AssertCapabilitiesStreamed(GinkgoT(), env)
	})
	It("advances the heartbeat", func() {
		suite.AssertHeartbeatAdvances(GinkgoT(), env)
	})
})
