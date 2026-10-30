//go:build e2e

// Golden-path e2e (CI gate): the full stack, provisioned AND asserted by
// this suite — kind cluster + git hostPath → operators → all helm charts
// (dependency-layered, concurrent) → Keycloak seeding → tenant → cluster
// registration → agent install, then the golden-path assertions: agent
// connect → capabilities streaming → RBAC materialization → policy
// evaluate → HA disruption (E2E_HA=1).
//
// Phase 3 of the shell→Go migration (parent design §3): golden-path.sh is
// gone. With no usable E2E_HANDOFF_PATH the suite provisions in-process;
// pointing E2E_HANDOFF_PATH at an existing handoff file attaches to an
// already-running stack instead (assert-only, no re-provisioning). Run:
//
//	KEEP_CLUSTER=true go test -tags=e2e -count=1 -timeout 45m ./...
//
// (from this directory — the module is nested, so a ./scripts/e2e/stack/...
// pattern from the repo root does not resolve; the -timeout matters because
// bring-up + assertions exceed go test's 10m default).
package stack_test

import (
	"testing"

	"7k-inari/inari-release-bundle/scripts/e2e/stack/suite"
)

func TestGoldenPath(t *testing.T) {
	suite.Run(t)
}

// TestRenderPath is the M5W2 operator-sunset Phase 1 validation: the
// server-rendered native resources + KRO instance sync Healthy via ArgoCD,
// RGD teardown cascades, agent status streaming works on the new GVRs, and
// addon schema validation rejects bad values with 422. Run against the
// golden-path stack (provision first or attach via E2E_HANDOFF_PATH):
//
//	KEEP_CLUSTER=true go test -tags=e2e -count=1 -timeout 45m -run TestRenderPath ./...
func TestRenderPath(t *testing.T) {
	suite.RenderPath(t)
}
