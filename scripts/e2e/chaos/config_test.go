package main

import (
	"strings"
	"testing"
)

func validEnv() map[string]string {
	return map[string]string{
		"CHAOS_SCENARIO":  "cnpg-primary-delete",
		"E2E_MODE":        "weekly",
		"E2E_SEEDED":      "true",
		"E2E_RUN_ID":      "123e4567-e89b-42d3-a456-426614174000",
		"E2E_NAMESPACE":   e2eNamespace,
		"E2E_BASE_DOMAIN": e2eDomain,
		"E2E_KC_URL":      e2eKCURL,
		"E2E_USER":        "dev-admin",
		"E2E_PASSWORD":    "secret",
		"E2E_TENANT_SLUG": "e2e-org",
		"E2E_HA":          "1",
	}
}

func getenvFor(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}

func TestLoadConfigGuards(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(map[string]string)
		want   string
	}{
		{name: "live namespace", mutate: func(e map[string]string) { e["E2E_NAMESPACE"] = "inari-system" }, want: "E2E_NAMESPACE"},
		{name: "prod namespace", mutate: func(e map[string]string) { e["E2E_NAMESPACE"] = "default" }, want: "E2E_NAMESPACE"},
		{name: "live domain", mutate: func(e map[string]string) { e["E2E_BASE_DOMAIN"] = "inari.7kgroup.org" }, want: "E2E_BASE_DOMAIN"},
		{name: "shared keycloak", mutate: func(e map[string]string) { e["E2E_KC_URL"] = "https://auth.7kgroup.org" }, want: "E2E_KC_URL"},
		{name: "non weekly", mutate: func(e map[string]string) { e["E2E_MODE"] = "release" }, want: "E2E_MODE"},
		{name: "unseeded", mutate: func(e map[string]string) { delete(e, "E2E_SEEDED") }, want: "E2E_SEEDED"},
		{name: "bad run id", mutate: func(e map[string]string) { e["E2E_RUN_ID"] = "../../live" }, want: "E2E_RUN_ID"},
		{name: "all is not a driver scenario", mutate: func(e map[string]string) { e["CHAOS_SCENARIO"] = "all" }, want: "workflow-only"},
		{name: "negative is not all", mutate: func(e map[string]string) { e["CHAOS_SCENARIO"] = "negative-control,all" }, want: "CHAOS_SCENARIO"},
		{name: "missing password", mutate: func(e map[string]string) { delete(e, "E2E_PASSWORD") }, want: "E2E_PASSWORD"},
		{name: "server requires ha", mutate: func(e map[string]string) { e["CHAOS_SCENARIO"] = "server-pod-delete"; e["E2E_HA"] = "0" }, want: "requires E2E_HA"},
		{name: "negative requires ha", mutate: func(e map[string]string) { e["CHAOS_SCENARIO"] = "negative-control"; e["E2E_HA"] = "false" }, want: "requires E2E_HA"},
		{name: "relative output", mutate: func(e map[string]string) { e["OUT_DIR"] = "artifacts" }, want: "absolute path"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := validEnv()
			tt.mutate(env)
			if _, err := loadConfig(getenvFor(env)); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("loadConfig error=%v, want substring %q", err, tt.want)
			}
		})
	}
}

func TestLoadConfigEveryScenario(t *testing.T) {
	for name := range scenarioRegistry {
		env := validEnv()
		env["CHAOS_SCENARIO"] = name
		cfg, err := loadConfig(getenvFor(env))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if cfg.Scenario.Name != name || cfg.ManifestDir == "" || cfg.UIDir == "" {
			t.Fatalf("%s: incomplete config %+v", name, cfg)
		}
	}
}
