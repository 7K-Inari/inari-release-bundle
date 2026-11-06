package kc

import (
	"os"
	"strings"
)

// Persona credential resolution (env contract:
// docs/e2e/environment-contract.md §1).
//
// On the persistent e2e environment persona passwords are Vault-random and
// injected via env (mounted from the inari-e2e-personas secret); the
// username-as-password fallback below exists ONLY as the documented
// kind/local-dev default, where the provisioner seeds exactly that
// convention. Same resolution shape as the seed step (scripts/e2e/seed)
// and the UI suite's helpers/env.ts.

// DevAdminCredentials resolves the suite's default identity (the dev-admin
// persona): E2E_USER (default "dev-admin") and E2E_PASSWORD. E2E_PASSWORD is
// required on the live env; unset it falls back to the username-as-password
// kind default so the local loop keeps working.
func DevAdminCredentials() (username, password string) {
	username = os.Getenv("E2E_USER")
	if username == "" {
		username = "dev-admin"
	}
	password = os.Getenv("E2E_PASSWORD")
	if password == "" {
		password = username // kind local-dev default (see doc comment)
	}
	return username, password
}

// PersonaPassword resolves the password of a fixed seeded persona
// (rbac-viewer, e2e-operator, ...): the per-persona contract variable
// E2E_<NAME>_PASSWORD (e2e- prefix stripped, dashes mapped to underscores,
// uppercased — e.g. rbac-viewer → E2E_RBAC_VIEWER_PASSWORD, matching
// scripts/e2e/seed's loadConfig), falling back to the username-as-password
// kind default.
func PersonaPassword(username string) string {
	name := strings.ToUpper(strings.ReplaceAll(strings.TrimPrefix(username, "e2e-"), "-", "_"))
	if pw := os.Getenv("E2E_" + name + "_PASSWORD"); pw != "" {
		return pw
	}
	return username
}
