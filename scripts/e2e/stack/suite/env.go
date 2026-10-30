//go:build e2e

// Package suite holds the golden-path provisioning + assertion helpers the
// Ginkgo specs (module root, *_test.go) compose into the labeled phases.
// provide.go, provision.go and seed.go bring the stack up in-process (kind
// local-dev path); the phases assert against it.
package suite

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"7k-inari/inari-release-bundle/scripts/e2e/stack/internal/fga"
	"7k-inari/inari-release-bundle/scripts/e2e/stack/internal/inariapi"
	"7k-inari/inari-release-bundle/scripts/e2e/stack/internal/kc"
	"7k-inari/inari-release-bundle/scripts/e2e/stack/internal/kube"
)

// GAP markers ported from golden-path.sh: each names an imperative
// workaround that maps to a tracked upstream fix. The constants exist so the
// upstream-fix tracking survives the port; remove a marker when its fix
// lands upstream.
const (
	// GapKCPlatformGroup: dev-admin is joined to the platform-admins
	// Keycloak group imperatively (by the suite's seed.go) because org_creator
	// tuple sync is driven by platform group membership; the upstream fix
	// automates this seeding in the realm/platform chart.
	GapKCPlatformGroup = "kc-platform-group"
	// GapKCAudienceMapper: the audience-inari-server protocol mapper is
	// ensured imperatively because the server validates aud=inari-server
	// and realm import does not yet create it.
	GapKCAudienceMapper = "aud-mapper"
	// GapRBACE2EArgoCD: the suite applies the materialized baseline/rbac/
	// bundle from the host, standing in for the tenant-local ArgoCD that
	// the e2e platform stack does not install.
	GapRBACE2EArgoCD = "rbac-e2e-argocd"
)

// Handoff is the contract the provisioner (provide.go) writes after
// bring-up; a later suite run attaching via E2E_HANDOFF_PATH consumes it
// read-only. Field renames must land in both writer and reader. On the live
// e2e environment the same fields arrive via the e2e-handoff ConfigMap
// (E2E_HANDOFF_CONFIGMAP) — same key names as the JSON fields.
type Handoff struct {
	Namespace     string `json:"namespace"`
	Toolbox       string `json:"toolbox"`
	Tenant        string `json:"tenant"`
	KCUID         string `json:"kc_uid"`     // dev-admin Keycloak user id
	OrgKCID       string `json:"org_kc_id"`  // tenant org's Keycloak organization id
	ClusterID     string `json:"cluster_id"` // registered e2e-self cluster id
	OrgID         string `json:"org_id"`     // cluster orgId (agent tenant id)
	GitHostDir    string `json:"git_host_dir"`
	AgentChartDir string `json:"agent_chart_dir"`
}

// Env carries the handoff plus ready clients and the HA selection.
type Env struct {
	Handoff
	HA            bool
	MigrationsDir string // E2E_MIGRATIONS_DIR (HA(c) migration count)
	KC            *kc.Client
	API           *inariapi.Client
	FGA           *fga.Client
}

// Load resolves the suite environment, in priority order:
//   - attach (file): E2E_HANDOFF_PATH points at an EXISTING handoff file
//     (written by an earlier provisioning run) — the suite asserts against
//     that already-running stack without re-provisioning.
//   - attach (ConfigMap): E2E_HANDOFF_CONFIGMAP is "namespace/name" of the
//     e2e-handoff ConfigMap written by the seed step on a persistent e2e
//     environment (the live-env attach mode; git_host_dir/agent_chart_dir
//     are kind-era fields and may be absent there).
//   - provision: otherwise the suite brings the whole stack up in-process
//     (kind → operators → charts → KC seeding → tenant → agent) via
//     Provision, writing the handoff to E2E_HANDOFF_PATH when set. This is
//     the local-dev inner loop; CI attach modes take precedence.
func Load(t TB) *Env {
	t.Helper()
	if path := os.Getenv("E2E_HANDOFF_PATH"); path != "" {
		if raw, err := os.ReadFile(path); err == nil {
			return loadHandoffFile(t, path, raw)
		}
	}
	if ref := os.Getenv("E2E_HANDOFF_CONFIGMAP"); ref != "" {
		return loadHandoffConfigMap(t, ref)
	}
	return Provision(t)
}

// loadHandoffFile attaches to a previously provisioned stack (read-only).
// File handoffs always come from a full kind-era provisioning, so every
// field is required.
func loadHandoffFile(t TB, path string, raw []byte) *Env {
	t.Helper()
	var h Handoff
	if err := json.Unmarshal(raw, &h); err != nil {
		t.Fatalf("parsing handoff %s: %v", path, err)
	}
	requireHandoffFields(t, "handoff "+path, h, true)
	e := newEnv(h)
	fmt.Printf("[e2e] handoff loaded from %s: ns=%s tenant=%s cluster=%s ha=%v\n",
		path, h.Namespace, h.Tenant, h.ClusterID, e.HA)
	return e
}

// loadHandoffConfigMap attaches to a persistent e2e environment whose seed
// step wrote the resolved handoff values to a ConfigMap (env contract §1.1).
// ref is "namespace/name". git_host_dir and agent_chart_dir are kind-era
// fields with no meaning on the live env and are optional here.
func loadHandoffConfigMap(t TB, ref string) *Env {
	t.Helper()
	ns, name, ok := strings.Cut(ref, "/")
	if !ok || ns == "" || name == "" {
		t.Fatalf("E2E_HANDOFF_CONFIGMAP must be \"namespace/name\", got %q", ref)
	}
	raw, err := kube.Kubectl("-n", ns, "get", "configmap", name, "-o", "json")
	if err != nil {
		t.Fatalf("reading handoff ConfigMap %s: %v", ref, err)
	}
	var cm struct {
		Data map[string]string `json:"data"`
	}
	if err := json.Unmarshal([]byte(raw), &cm); err != nil {
		t.Fatalf("parsing handoff ConfigMap %s: %v", ref, err)
	}
	h := Handoff{
		Namespace:     cm.Data["namespace"],
		Toolbox:       cm.Data["toolbox"],
		Tenant:        cm.Data["tenant"],
		KCUID:         cm.Data["kc_uid"],
		OrgKCID:       cm.Data["org_kc_id"],
		ClusterID:     cm.Data["cluster_id"],
		OrgID:         cm.Data["org_id"],
		GitHostDir:    cm.Data["git_host_dir"],
		AgentChartDir: cm.Data["agent_chart_dir"],
	}
	requireHandoffFields(t, "handoff ConfigMap "+ref, h, false)
	e := newEnv(h)
	fmt.Printf("[e2e] handoff loaded from ConfigMap %s: ns=%s tenant=%s cluster=%s ha=%v\n",
		ref, h.Namespace, h.Tenant, h.ClusterID, e.HA)
	return e
}

// requireHandoffFields fails fast on an incomplete handoff. The kind-era
// fields (git_host_dir, agent_chart_dir) are required only for file
// handoffs; ConfigMap handoffs from a live env legitimately omit them.
func requireHandoffFields(t TB, source string, h Handoff, kindFields bool) {
	t.Helper()
	fields := map[string]string{
		"namespace": h.Namespace, "toolbox": h.Toolbox, "tenant": h.Tenant,
		"kc_uid": h.KCUID, "org_kc_id": h.OrgKCID, "cluster_id": h.ClusterID,
		"org_id": h.OrgID,
	}
	if kindFields {
		fields["git_host_dir"] = h.GitHostDir
		fields["agent_chart_dir"] = h.AgentChartDir
	}
	for field, v := range fields {
		if v == "" {
			t.Fatalf("%s: field %q is empty (stale handoff? re-run the provisioning/seed step)", source, field)
		}
	}
}

// newEnv builds the Env (clients + HA selection) around a handoff.
func newEnv(h Handoff) *Env {
	e := &Env{
		Handoff:       h,
		HA:            os.Getenv("E2E_HA") == "1",
		MigrationsDir: migrationsDir(),
	}
	e.KC = &kc.Client{NS: h.Namespace, Toolbox: h.Toolbox}
	e.FGA = &fga.Client{NS: h.Namespace, Toolbox: h.Toolbox}
	e.API = &inariapi.Client{
		NS: h.Namespace, Toolbox: h.Toolbox, Tenant: h.Tenant,
		Token: e.KC.UserToken,
	}
	return e
}

// migrationsDir resolves the inari-server migrations mount for the HA(c)
// migration-count check: E2E_MIGRATIONS_DIR (env contract §3.2) with
// SERVER_MIGRATIONS_DIR kept as the legacy alias until CI switches over.
func migrationsDir() string {
	if dir := os.Getenv("E2E_MIGRATIONS_DIR"); dir != "" {
		return dir
	}
	return os.Getenv("SERVER_MIGRATIONS_DIR")
}

// RequireHAMigrations fails fast when the HA leg lacks the migrations dir.
func (e *Env) RequireHAMigrations(t TB) {
	t.Helper()
	if e.HA && e.MigrationsDir == "" {
		t.Fatal("E2E_HA=1 requires E2E_MIGRATIONS_DIR pointing at inari-server's internal/db/migrations")
	}
}
