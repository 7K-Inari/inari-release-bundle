//go:build e2e

package stack_test

import (
	. "github.com/onsi/ginkgo/v2"

	"7k-inari/inari-release-bundle/scripts/e2e/stack/suite"
)

// Policy evaluate phase: the deny-latest-image matrix — :latest/untagged
// denied with the deny-latest-image rule, pinned tag/digest allowed, and a
// disabled policy must not gate the request (negative control). Ordered: the
// specs share the policy created in BeforeAll.
var _ = Describe("Policy evaluate", Label(suite.LabelP0), Label(suite.LabelPolicy), Ordered, func() {
	var policyID string

	BeforeAll(func() {
		policyID = suite.CreateDenyLatestPolicy(GinkgoT(), env)
	})

	It("denies :latest and untagged images with the deny-latest-image rule", func() {
		suite.AssertPolicyDenyMatrix(GinkgoT(), env)
	})
	It("allows pinned tags and digests", func() {
		suite.AssertPolicyAllowMatrix(GinkgoT(), env)
	})
	It("does not gate when the policy is disabled (negative control)", func() {
		suite.AssertDisabledPolicyDoesNotGate(GinkgoT(), env, policyID)
	})
})
