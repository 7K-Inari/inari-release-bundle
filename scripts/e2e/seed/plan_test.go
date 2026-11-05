// Unit tests for the pure decision logic (ported 1:1 from the retired
// scripts/e2e/ui/seed/seed-personas.test.mjs). HTTP/Kubernetes side effects
// are covered by httptest-driven client tests and the live-env run.
package main

import (
	"reflect"
	"strings"
	"testing"
)

func TestPersonas(t *testing.T) {
	want := []Persona{
		{Username: "e2e-operator", Role: "operator"},
		{Username: "e2e-editor", Role: "editor"},
		{Username: "e2e-viewer", Role: "viewer"},
		{Username: "e2e-member", Role: ""},
	}
	if !reflect.DeepEqual(Personas, want) {
		t.Fatalf("Personas = %+v, want %+v", Personas, want)
	}
}

func TestMergeClientRedirects(t *testing.T) {
	const origin = "http://127.0.0.1:8080"

	t.Run("adds the browser origin to a client that lacks it", func(t *testing.T) {
		client := map[string]any{
			"redirectUris": []any{"http://localhost/*"},
			"webOrigins":   []any{"+"},
		}
		body, changed := mergeClientRedirects(client, origin)
		if !changed {
			t.Fatal("changed = false, want true")
		}
		if got := body["redirectUris"]; !reflect.DeepEqual(got, []string{"http://localhost/*", origin + "/*"}) {
			t.Fatalf("redirectUris = %v", got)
		}
		if got := body["webOrigins"]; !reflect.DeepEqual(got, []string{"+", origin}) {
			t.Fatalf("webOrigins = %v", got)
		}
	})

	t.Run("no-op when the origin is already present (idempotent rerun)", func(t *testing.T) {
		client := map[string]any{
			"redirectUris": []string{origin + "/*"},
			"webOrigins":   []string{origin},
		}
		body, changed := mergeClientRedirects(client, origin)
		if changed {
			t.Fatal("changed = true, want false")
		}
		if got := body["redirectUris"]; !reflect.DeepEqual(got, []string{origin + "/*"}) {
			t.Fatalf("redirectUris = %v", got)
		}
	})

	t.Run("tolerates missing arrays on the client representation", func(t *testing.T) {
		body, changed := mergeClientRedirects(map[string]any{}, origin)
		if !changed {
			t.Fatal("changed = false, want true")
		}
		if got := body["redirectUris"]; !reflect.DeepEqual(got, []string{origin + "/*"}) {
			t.Fatalf("redirectUris = %v", got)
		}
		if got := body["webOrigins"]; !reflect.DeepEqual(got, []string{origin}) {
			t.Fatalf("webOrigins = %v", got)
		}
	})
}

func TestRequireBuiltinRoles(t *testing.T) {
	roles := []Role{
		{ID: "r-admin", Name: "admin", Builtin: true},
		{ID: "r-operator", Name: "operator", Builtin: true},
		{ID: "r-editor", Name: "editor", Builtin: true},
		{ID: "r-viewer", Name: "viewer", Builtin: true},
		{ID: "r-custom", Name: "custom-x", Builtin: false},
	}

	t.Run("maps every built-in role name to its id", func(t *testing.T) {
		got, err := requireBuiltinRoles(roles)
		if err != nil {
			t.Fatal(err)
		}
		want := map[string]string{
			"admin": "r-admin", "operator": "r-operator",
			"editor": "r-editor", "viewer": "r-viewer",
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("requireBuiltinRoles = %v, want %v", got, want)
		}
	})

	t.Run("fails loudly when a built-in is missing (P0-class signal)", func(t *testing.T) {
		broken := roles[:len(roles)-2] // drops viewer and the custom role
		if _, err := requireBuiltinRoles(broken); err == nil {
			t.Fatal("expected an error naming the missing role")
		} else if got := err.Error(); !strings.Contains(got, "viewer") {
			t.Fatalf("error %q does not name the missing role", got)
		}
	})
}

func TestPlanRoleAssignments(t *testing.T) {
	ids := map[string]string{
		"e2e-operator": "u-op", "e2e-editor": "u-ed",
		"e2e-viewer": "u-vi", "e2e-member": "u-me",
	}

	t.Run("assigns roles only to members that lack them", func(t *testing.T) {
		members := []Member{
			{UserID: "u-op", Roles: []string{"operator"}}, // already seeded
			{UserID: "u-ed", Roles: nil},
			// u-vi not yet projected into the server (KC join still propagating)
			{UserID: "u-me", Roles: nil},
		}
		plan := planRoleAssignments(Personas, ids, members)
		want := []Assignment{
			{Username: "e2e-editor", UserID: "u-ed", Role: "editor"},
			{Username: "e2e-viewer", UserID: "u-vi", Role: "viewer"},
		}
		if !reflect.DeepEqual(plan.Assignments, want) {
			t.Fatalf("Assignments = %+v, want %+v", plan.Assignments, want)
		}
		if !reflect.DeepEqual(plan.Skipped, []string{"e2e-operator"}) {
			t.Fatalf("Skipped = %v", plan.Skipped)
		}
		if plan.MemberLeak != nil {
			t.Fatalf("MemberLeak = %v, want nil", plan.MemberLeak)
		}
	})

	t.Run("flags a role on e2e-member as a spec leak (never auto-fixed)", func(t *testing.T) {
		members := []Member{{UserID: "u-me", Roles: []string{"viewer"}}}
		plan := planRoleAssignments(Personas, ids, members)
		if len(plan.Assignments) != 3 {
			t.Fatalf("Assignments = %d, want 3", len(plan.Assignments))
		}
		if !reflect.DeepEqual(plan.MemberLeak, []string{"viewer"}) {
			t.Fatalf("MemberLeak = %v", plan.MemberLeak)
		}
	})
}
