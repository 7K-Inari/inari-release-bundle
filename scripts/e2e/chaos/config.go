package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const (
	e2eNamespace = "inari-e2e"
	e2eDomain    = "inari-e2e.7kgroup.org"
	e2eKCURL     = "https://auth-e2e.7kgroup.org"
)

// Scenario describes one independently selectable weekly workflow leg.
type Scenario struct {
	Name         string
	Engines      []string
	SmokeOnly    bool
	Negative     bool
	RequiresHA   bool
	RequiresCNPG bool
}

var scenarioRegistry = map[string]Scenario{
	"cnpg-primary-delete": {Name: "cnpg-primary-delete", Engines: []string{"cnpg-primary-delete"}, RequiresCNPG: true},
	"keycloak-latency":    {Name: "keycloak-latency", Engines: []string{"keycloak-latency"}},
	"nats-outage":         {Name: "nats-outage", Engines: []string{"nats-outage"}, SmokeOnly: true},
	"openfga-latency":     {Name: "openfga-latency", Engines: []string{"openfga-latency", "openfga-timeout"}},
	"server-pod-delete":   {Name: "server-pod-delete", Engines: []string{"server-pod-delete"}, RequiresHA: true},
	"negative-control":    {Name: "negative-control", Engines: []string{"negative-control"}, Negative: true, RequiresHA: true},
}

// Config contains only non-secret paths plus the persona credential inherited
// from the existing e2e secret. Credentials are never included in evidence.
type Config struct {
	Scenario    Scenario
	RunID       string
	Namespace   string
	BaseDomain  string
	KCURL       string
	User        string
	Password    string
	TenantSlug  string
	HA          bool
	OutDir      string
	EvidenceDir string
	BundleDir   string
	ManifestDir string
	UIDir       string
	Kubectl     string
}

var runIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

func envOr(getenv func(string) string, key, fallback string) string {
	if value := getenv(key); value != "" {
		return value
	}
	return fallback
}

func loadConfig(getenv func(string) string) (Config, error) {
	name := getenv("CHAOS_SCENARIO")
	scenario, ok := scenarioRegistry[name]
	if !ok {
		return Config{}, fmt.Errorf("CHAOS_SCENARIO must name exactly one supported scenario (got %q; all is workflow-only)", name)
	}
	cfg := Config{
		Scenario:    scenario,
		RunID:       getenv("E2E_RUN_ID"),
		Namespace:   getenv("E2E_NAMESPACE"),
		BaseDomain:  getenv("E2E_BASE_DOMAIN"),
		KCURL:       getenv("E2E_KC_URL"),
		User:        envOr(getenv, "E2E_USER", "dev-admin"),
		Password:    getenv("E2E_PASSWORD"),
		TenantSlug:  envOr(getenv, "E2E_TENANT_SLUG", "e2e-org"),
		HA:          getenv("E2E_HA") == "true" || getenv("E2E_HA") == "1",
		OutDir:      envOr(getenv, "OUT_DIR", "/artifacts/out"),
		EvidenceDir: envOr(getenv, "EVIDENCE_DIR", "/artifacts/evidence"),
		BundleDir:   envOr(getenv, "BUNDLE_DIR", "/work"),
		Kubectl:     envOr(getenv, "KUBECTL", "kubectl"),
	}
	cfg.ManifestDir = envOr(getenv, "CHAOS_MANIFEST_DIR", filepath.Join(cfg.BundleDir, "scripts/e2e/chaos/manifests"))
	cfg.UIDir = envOr(getenv, "UI_DIR", filepath.Join(cfg.BundleDir, "scripts/e2e/ui"))

	if getenv("E2E_MODE") != "weekly" {
		return cfg, fmt.Errorf("E2E_MODE must be weekly (got %q)", getenv("E2E_MODE"))
	}
	if getenv("E2E_SEEDED") != "true" {
		return cfg, fmt.Errorf("E2E_SEEDED must be true; chaos never runs against an unseeded environment")
	}
	if !runIDPattern.MatchString(cfg.RunID) {
		return cfg, fmt.Errorf("E2E_RUN_ID must be a lowercase Kubernetes-safe run id (got %q)", cfg.RunID)
	}
	// These are intentionally exact equality checks, not defaults. A typo must
	// stop before the first Kubernetes read or mutation.
	if cfg.Namespace != e2eNamespace {
		return cfg, fmt.Errorf("E2E_NAMESPACE must be %q (got %q)", e2eNamespace, cfg.Namespace)
	}
	if cfg.BaseDomain != e2eDomain {
		return cfg, fmt.Errorf("E2E_BASE_DOMAIN must be %q (got %q)", e2eDomain, cfg.BaseDomain)
	}
	if cfg.KCURL != e2eKCURL {
		return cfg, fmt.Errorf("E2E_KC_URL must be %q (got %q)", e2eKCURL, cfg.KCURL)
	}
	if cfg.User == "" || cfg.Password == "" {
		return cfg, fmt.Errorf("E2E_USER/E2E_PASSWORD are required")
	}
	if cfg.TenantSlug == "" {
		return cfg, fmt.Errorf("E2E_TENANT_SLUG is required")
	}
	if cfg.Scenario.RequiresHA && !cfg.HA {
		return cfg, fmt.Errorf("scenario %s requires E2E_HA=1 (two server replicas)", cfg.Scenario.Name)
	}
	for key, value := range map[string]string{"OUT_DIR": cfg.OutDir, "EVIDENCE_DIR": cfg.EvidenceDir, "BUNDLE_DIR": cfg.BundleDir, "UI_DIR": cfg.UIDir, "CHAOS_MANIFEST_DIR": cfg.ManifestDir} {
		if !filepath.IsAbs(value) {
			return cfg, fmt.Errorf("%s must be an absolute path (got %q)", key, value)
		}
	}
	if strings.ContainsRune(cfg.Kubectl, '\x00') {
		return cfg, fmt.Errorf("KUBECTL contains a NUL byte")
	}
	return cfg, nil
}

func shortRunID(runID string) string {
	if len(runID) <= 8 {
		return runID
	}
	return runID[:8]
}

func scenarioName(name string) string {
	if _, ok := scenarioRegistry[name]; ok {
		return name
	}
	return "configuration"
}

// Ensure the artifact paths are created before config validation so even an
// invalid invocation produces a ReportPortal-visible JUnit failure.
func artifactDirs(getenv func(string) string) (string, string) {
	return envOr(getenv, "OUT_DIR", "/artifacts/out"), envOr(getenv, "EVIDENCE_DIR", "/artifacts/evidence")
}

func makeArtifactDirs(outDir, evidenceDir string) error {
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}
	return os.MkdirAll(evidenceDir, 0o755)
}
