//go:build e2e

// Unit tests for the render-path harness's pure helpers. They run under the
// e2e tag (the suite package is e2e-only) but need no cluster — same
// pattern as provision_unit_test.go.
package suite

import (
	"strings"
	"testing"
)

func TestRenderStatusWatches(t *testing.T) {
	watches := renderStatusWatches()
	for _, want := range []string{
		"stacks.inari.io/v1alpha1/tenantnamespaces",   // KRO instance (no default)
		"identity.inari.io/v1alpha1/tenantrealms",     // claim (no default)
		"identity.inari.io/v1alpha1/tenantclients",    // claim (no default)
		"cert-manager.io/v1/clusterissuers",           // native replacement
		"externaldns.k8s.io/v1alpha1/dnsendpoints",    // native replacement
		"platform.inari.io/v1alpha1/tenantnamespaces", // legacy dual-run entry
	} {
		if !strings.Contains(watches, want) {
			t.Errorf("watch list missing %q: %s", want, watches)
		}
	}
	// INARI_STATUS_WATCHES overrides the defaults wholesale, so the list
	// must be parse-complete: semicolon-separated group/version/resource.
	for entry := range strings.SplitSeq(watches, ";") {
		if parts := strings.Split(entry, "/"); len(parts) != 3 {
			t.Errorf("malformed watch entry %q (want group/version/resource)", entry)
		}
	}
}
