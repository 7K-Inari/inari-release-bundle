package kc

import (
	"os"
	"testing"
)

// unsetenv clears keys for the duration of the test (restoring any prior
// values), so ambient CI env cannot leak into the "unset" cases.
func unsetenv(t *testing.T, keys ...string) {
	t.Helper()
	for _, k := range keys {
		if v, ok := os.LookupEnv(k); ok {
			t.Cleanup(func() { os.Setenv(k, v) })
		} else {
			t.Cleanup(func() { os.Unsetenv(k) })
		}
		os.Unsetenv(k)
	}
}

func TestDevAdminCredentials(t *testing.T) {
	for _, tc := range []struct {
		name         string
		envUser      string // "" = leave unset
		envPassword  string // "" = leave unset
		wantUser     string
		wantPassword string
	}{
		{"both unset: documented kind default", "", "", "dev-admin", "dev-admin"},
		{"env user only: username-as-password fallback", "ci-admin", "", "ci-admin", "ci-admin"},
		{"env password only: default user", "", "s3cret", "dev-admin", "s3cret"},
		{"env pair wins (live env)", "ci-admin", "s3cret", "ci-admin", "s3cret"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			unsetenv(t, "E2E_USER", "E2E_PASSWORD")
			if tc.envUser != "" {
				t.Setenv("E2E_USER", tc.envUser)
			}
			if tc.envPassword != "" {
				t.Setenv("E2E_PASSWORD", tc.envPassword)
			}
			u, p := DevAdminCredentials()
			if u != tc.wantUser || p != tc.wantPassword {
				t.Fatalf("DevAdminCredentials() = (%q, %q), want (%q, %q)", u, p, tc.wantUser, tc.wantPassword)
			}
		})
	}
}

func TestPersonaPassword(t *testing.T) {
	for _, tc := range []struct {
		name     string
		username string
		envKey   string
		envValue string // "" = leave unset
		want     string
	}{
		{"env unset: username-as-password kind default", "rbac-viewer", "E2E_RBAC_VIEWER_PASSWORD", "", "rbac-viewer"},
		{"env var wins (live env)", "rbac-viewer", "E2E_RBAC_VIEWER_PASSWORD", "vault-random", "vault-random"},
		{"e2e- prefix stripped like the seed step", "e2e-operator", "E2E_OPERATOR_PASSWORD", "vault-op", "vault-op"},
		{"e2e- persona without env falls back to username", "e2e-viewer", "E2E_VIEWER_PASSWORD", "", "e2e-viewer"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			unsetenv(t, tc.envKey)
			if tc.envValue != "" {
				t.Setenv(tc.envKey, tc.envValue)
			}
			if got := PersonaPassword(tc.username); got != tc.want {
				t.Fatalf("PersonaPassword(%q) = %q, want %q", tc.username, got, tc.want)
			}
		})
	}
}
