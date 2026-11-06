//go:build e2e

// Unit tests for the state-repo access helpers (kind hostPath vs live-env
// git daemon). They run under the e2e tag but need no cluster — same
// pattern as provision_unit_test.go.
package suite

import "testing"

func TestStateRepoURL(t *testing.T) {
	e := &Env{Handoff: Handoff{Tenant: "e2e-org"}, GitURL: "git://git-service.inari-e2e.svc:9418"}
	got := e.stateRepoURL()
	want := "git://git-service.inari-e2e.svc:9418/e2e-org-inari-state.git"
	if got != want {
		t.Fatalf("stateRepoURL = %q, want %q", got, want)
	}
}

func TestStateRepoLocalOnKind(t *testing.T) {
	// Kind path: GitHostDir set → the hostPath mount, no git daemon.
	e := &Env{Handoff: Handoff{Tenant: "e2e-org", GitHostDir: "/tmp/inari-e2e-git"}}
	if got := e.stateRepo(); got != "/tmp/inari-e2e-git/e2e-org-inari-state.git" {
		t.Fatalf("stateRepo = %q", got)
	}
	// Live path: GitHostDir empty (ConfigMap handoff omits it) → the suite
	// must read via E2E_GIT_URL, never a relative hostPath.
	live := &Env{Handoff: Handoff{Tenant: "e2e-org"}, GitURL: "git://git:9418"}
	if live.stateRepoIsLocal() {
		t.Fatal("stateRepoIsLocal = true with an empty GitHostDir — live env must use the git daemon")
	}
	kind := &Env{Handoff: Handoff{Tenant: "e2e-org", GitHostDir: "/tmp/g"}}
	if !kind.stateRepoIsLocal() {
		t.Fatal("stateRepoIsLocal = false with GitHostDir set — kind must keep the hostPath")
	}
}
