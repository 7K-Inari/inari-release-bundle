//go:build e2e

// Unit tests for the policy-phase pure helpers. Same pattern as
// provision_unit_test.go: e2e tag, no cluster.
package suite

import (
	"testing"

	"7k-inari/inari-release-bundle/scripts/e2e/stack/internal/inariapi"
)

func TestFindPolicyIDByName(t *testing.T) {
	policies := []inariapi.PolicySummary{
		{ID: "p1", Name: "deny-latest-image"},
		{ID: "p2", Name: "require-owner"},
	}
	if got := findPolicyIDByName(policies, "deny-latest-image"); got != "p1" {
		t.Fatalf("findPolicyIDByName = %q, want p1", got)
	}
	// Absent on a fresh (kind) env → "" selects the create path.
	if got := findPolicyIDByName(policies, "no-such-policy"); got != "" {
		t.Fatalf("findPolicyIDByName = %q, want empty for a missing policy", got)
	}
	// Exact match only — a prefix must not collide.
	if got := findPolicyIDByName(policies, "deny-latest"); got != "" {
		t.Fatalf("findPolicyIDByName = %q, want empty for a prefix", got)
	}
}
