//go:build e2e

// Golden-path e2e (CI gate): the full stack, provisioned AND asserted by
// this suite — kind cluster + git hostPath → operators → all helm charts
// (dependency-layered, concurrent) → Keycloak seeding → tenant → cluster
// registration → agent install, then the golden-path assertions: agent
// connect → capabilities streaming → RBAC materialization → policy
// evaluate → HA disruption (E2E_HA=1).
//
// The suite is a Ginkgo v2 suite: specs live in the sibling *_test.go files
// of this package, labeled with the canonical taxonomy (suite/labels.go).
// Phase bodies stay in the suite package as TB-based helpers — structure
// changed, behavior preserved.
//
// Environment resolution (suite.Load, in BeforeSuite): with no usable
// E2E_HANDOFF_PATH/E2E_HANDOFF_CONFIGMAP the suite provisions in-process;
// pointing E2E_HANDOFF_PATH at an existing handoff file (or
// E2E_HANDOFF_CONFIGMAP at the live env's e2e-handoff ConfigMap) attaches
// assert-only instead. Run:
//
//	KEEP_CLUSTER=true go test -tags=e2e -count=1 -timeout 45m ./...
//
// (from this directory — the module is nested, so a ./scripts/e2e/stack/...
// pattern from the repo root does not resolve; the -timeout matters because
// bring-up + assertions exceed go test's 10m default). Ginkgo flags pass
// through -args, e.g.:
//
//	go test -tags=e2e -count=1 -timeout 45m . \
//	  -args --ginkgo.label-filter="(p0||smoke)&&!quarantine" \
//	        --ginkgo.junit-report=junit.xml
package stack_test

import (
	"os"
	"testing"

	. "github.com/onsi/ginkgo/v2"

	"7k-inari/inari-release-bundle/scripts/e2e/stack/suite"
)

// env is resolved exactly once in BeforeSuite: attach (E2E_HANDOFF_PATH file
// or E2E_HANDOFF_CONFIGMAP) or provision in-process (kind local-dev path).
// The golden path is inherently sequential — later phases depend on earlier
// state — and a failed setup aborts the whole suite.
var env *suite.Env

func TestGoldenPath(t *testing.T) {
	RunSpecs(t, "Golden-Path Suite")
}

var _ = BeforeSuite(func() {
	env = suite.Load(GinkgoT())
	if os.Getenv("E2E_PROVISION_ONLY") == "1" {
		Skip("E2E_PROVISION_ONLY=1: stack provisioned; skipping assertions " +
			"(nightly bring-up parity with the old script)")
	}
	env.RequireHAMigrations(GinkgoT())
})
