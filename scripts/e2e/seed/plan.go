// Pure decision logic for the persona seed — ported 1:1 from the retired
// scripts/e2e/ui/seed/seed-personas.mjs (same semantics, Go types). The
// side-effecting phases live in main.go; everything here is unit-tested.
package main

import (
	"fmt"
	"slices"
	"strings"
)

// Persona is one fixed e2e identity: a Keycloak user joined to the tenant's
// KC organization, with an optional built-in role assigned through the
// inari-server membership API (never KC groups, never direct DB — seeding
// doubles as a membership-path P0 signal). One per ADR-0013 built-in role,
// plus the role-less propagation target. Passwords are NOT carried here:
// they come from the E2E_*_PASSWORD contract variables (see main.go).
//
// RULES (also in scripts/e2e/ui/README.md): specs must NEVER mutate the
// role assignments of e2e-operator/e2e-editor/e2e-viewer, and never touch
// dev-admin. e2e-member is the only persona whose role may change; the
// role-lifecycle spec owns it and must restore it to role-less. The seed
// detects a leaked role on e2e-member and warns, but never silently
// "repairs" spec state.
var Personas = []Persona{
	{Username: "e2e-operator", Role: "operator"}, // Platform Engineer
	{Username: "e2e-editor", Role: "editor"},     // Developer
	{Username: "e2e-viewer", Role: "viewer"},     // read-only RBAC surface
	{Username: "e2e-member", Role: ""},           // org member with NO role/team
}

// Persona is a fixed seeded identity; Role "" means deliberately role-less.
type Persona struct {
	Username string
	Role     string
}

// Role is the subset of GET /tenants/{org}/roles entries the seed reads.
type Role struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Builtin bool   `json:"builtin"`
}

// Member is the subset of GET /tenants/{org}/members entries the seed reads.
type Member struct {
	UserID string   `json:"userId"`
	Roles  []string `json:"roles"`
}

// Assignment is one pending role grant through the membership API.
type Assignment struct {
	Username string
	UserID   string
	Role     string
}

// RolePlan is the outcome of planRoleAssignments.
type RolePlan struct {
	Assignments []Assignment
	Skipped     []string
	// MemberLeak lists roles found on e2e-member (a spec leaked an
	// assignment); nil when e2e-member is role-less as required.
	MemberLeak []string
}

// builtinRoles are the ADR-0013 built-ins (bundles verified in inari-server
// migration 0029_roles.sql).
var builtinRoles = []string{"admin", "operator", "editor", "viewer"}

// mergeClientRedirects merges the browser origin into a KC client
// representation for the inari-ui redirect whitelist. Returns the full
// client object to PUT back (all other fields preserved) and whether
// anything changed. The realm re-import wipes browser-origin
// redirectUris/webOrigins, so the seed re-merges them after every env-prep
// (hard rule §8.4) — the whitelist is owned here, never in the workflow.
func mergeClientRedirects(client map[string]any, origin string) (map[string]any, bool) {
	merged := make(map[string]any, len(client))
	for k, v := range client {
		merged[k] = v
	}
	changed := false

	redirects := stringSlice(client["redirectUris"])
	if !slices.Contains(redirects, origin+"/*") {
		redirects = append(redirects, origin+"/*")
		changed = true
	}
	origins := stringSlice(client["webOrigins"])
	if !slices.Contains(origins, origin) {
		origins = append(origins, origin)
		changed = true
	}
	merged["redirectUris"] = redirects
	merged["webOrigins"] = origins
	return merged, changed
}

// stringSlice coerces a decoded JSON array ([]any), an already-typed
// []string, or nil to []string.
func stringSlice(v any) []string {
	switch items := v.(type) {
	case []string:
		return append([]string(nil), items...)
	case []any:
		out := make([]string, 0, len(items))
		for _, it := range items {
			if s, ok := it.(string); ok {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

// requireBuiltinRoles maps built-in role names to their ids from
// GET /tenants/{org}/roles. It fails fast when any built-in is missing — a
// broken roles endpoint or bundle is a P0-class signal, not a seed bug.
func requireBuiltinRoles(roles []Role) (map[string]string, error) {
	byName := make(map[string]string, len(builtinRoles))
	for _, name := range builtinRoles {
		var found *Role
		for i := range roles {
			if roles[i].Name == name && roles[i].Builtin {
				found = &roles[i]
				break
			}
		}
		if found == nil {
			names := make([]string, 0, len(roles))
			for _, r := range roles {
				names = append(names, r.Name)
			}
			return nil, fmt.Errorf(
				"built-in role %q missing from GET /tenants/{org}/roles (got: %s) — membership path is broken",
				name, strings.Join(names, ", "))
		}
		byName[name] = found.ID
	}
	return byName, nil
}

// planRoleAssignments decides which role grants are still needed. userIDs
// maps persona username -> KC user id; members is GET /tenants/{org}/members
// output. A persona not yet projected into the server (KC org join still
// propagating) is simply planned for assignment — the caller retries the
// PUT on 404 until the projection catches up.
func planRoleAssignments(personas []Persona, userIDs map[string]string, members []Member) RolePlan {
	byUserID := make(map[string][]string, len(members))
	for _, m := range members {
		byUserID[m.UserID] = m.Roles
	}
	plan := RolePlan{}
	for _, p := range personas {
		held := byUserID[userIDs[p.Username]]
		if p.Role == "" {
			if len(held) > 0 {
				plan.MemberLeak = held
			}
			continue
		}
		if slices.Contains(held, p.Role) {
			plan.Skipped = append(plan.Skipped, p.Username)
		} else {
			plan.Assignments = append(plan.Assignments, Assignment{
				Username: p.Username, UserID: userIDs[p.Username], Role: p.Role,
			})
		}
	}
	return plan
}
