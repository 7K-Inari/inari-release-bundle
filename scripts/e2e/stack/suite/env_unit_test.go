//go:build e2e

// Unit tests for the handoff normalization (env contract defaults). They run
// under the e2e tag (the suite package is e2e-only) but need no cluster —
// same pattern as provision_unit_test.go.
package suite

import "testing"

func TestNormalizeHandoffDefaultsAgentNamespace(t *testing.T) {
	// Kind file handoffs and pre-agent_namespace ConfigMaps carry no
	// agent_namespace: the default keeps the historical kind behavior
	// (agent in inari-system) instead of failing the attach.
	h := normalizeHandoff(Handoff{Namespace: "inari"})
	if h.AgentNamespace != "inari-system" {
		t.Fatalf("AgentNamespace = %q, want inari-system default", h.AgentNamespace)
	}
}

func TestNormalizeHandoffKeepsPublishedAgentNamespace(t *testing.T) {
	// The live e2e env publishes agent_namespace=inari-e2e-agent (the agent
	// chart's namespace; INARI_ESO_SECRET_NAMESPACE) — it must survive.
	h := normalizeHandoff(Handoff{Namespace: "inari-e2e", AgentNamespace: "inari-e2e-agent"})
	if h.AgentNamespace != "inari-e2e-agent" {
		t.Fatalf("AgentNamespace = %q, want inari-e2e-agent (published value kept)", h.AgentNamespace)
	}
}
