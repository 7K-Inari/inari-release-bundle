// Persona seed for the Inari e2e environment — IDEMPOTENT, safe to re-run
// after every env-prep. Runs as a workflow step between env-prep and the
// suite legs (env contract §3.5 / docs/e2e/environment-contract.md §1.1).
//
// It owns six things:
//
//  1. The inari-ui client redirect whitelist (https://$E2E_BASE_DOMAIN/*):
//     the realm re-import wipes browser-origin redirectUris/webOrigins, so
//     they are re-merged here (hard rule §8.4 — never in the workflow).
//  2. The realm-scope repair (GAP markers, scopes.go): the realm import's
//     explicit clientScopes array suppresses Keycloak's built-in scopes;
//     without basic+organization attached to inari-server and inari-ui,
//     EVERY token request dies with invalid_scope (verified live
//     2026-11-05). The A4 probe (dev-admin token must carry sub +
//     aud=inari-server) is the tripwire.
//  3. The five fixed personas as Keycloak users (passwords converged from
//     the E2E_*_PASSWORD contract variables — the Vault-backed
//     inari-e2e-personas secret on the live env), each joined to the
//     tenant's KC organization so the organization:* scope works.
//  4. Role assignment THROUGH THE INARI-SERVER API
//     (PUT /api/v1/tenants/{org}/members/{subject} {roleId}) — never KC
//     groups, never direct DB — so seeding exercises the real membership
//     path. A broken roles/members API fails the seed loudly: P0-class
//     signal, not a seed bug.
//  5. The default tenant + cluster records and the default git-config, plus
//     the git write-path proof (reconcile → git-daemon ls-remote) when
//     E2E_GIT_URL is set.
//  6. The e2e-handoff ConfigMap (E2E_HANDOFF_CONFIGMAP=namespace/name) with
//     the resolved discovered values, incl. ensuring the toolbox pod the
//     Ginkgo suite uses as its curl transport.
//
// Without E2E_HANDOFF_CONFIGMAP the seed runs in LOCAL mode (kind dev
// loop): phases A–C only — the provisioner owns the kind handoff file, so
// the seed never writes one.
//
// Persona rules (authoritative in scripts/e2e/ui/README.md): specs must
// never mutate e2e-operator/e2e-editor/e2e-viewer assignments or dev-admin;
// e2e-member is owned by the role-lifecycle spec which must restore it
// role-less — the seed warns on a leak but never repairs spec state.
//
// Besides the personas the seed converges ONE non-persona identity:
// e2e-ops (E2E_OPS_USER/E2E_OPS_PASSWORD), a platform-admins member used by
// the janitor's approve step (a requester may never decide their own
// decommission — four-eyes). It carries no role assignments and no suite
// consumes it.
package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

type config struct {
	KCURL      string // E2E_KC_URL
	Realm      string // fixed: inari
	UIOrigin   string // https://$E2E_BASE_DOMAIN
	APIBase    string // https://$E2E_BASE_DOMAIN/api/v1 (API_BASE override kept for local debugging)
	TenantName string // E2E_TENANT_NAME
	TenantSlug string // E2E_TENANT_SLUG
	Namespace  string // E2E_NAMESPACE — handoff/toolbox/secret namespace
	GitURL     string // E2E_GIT_URL ("" disables the B4 proof)

	User     string // E2E_USER (dev-admin)
	Password string // E2E_PASSWORD
	// PersonaPasswords maps persona username -> password.
	PersonaPasswords map[string]string

	// OpsUser/OpsPassword are the NON-persona platform-ops identity
	// (E2E_OPS_USER/E2E_OPS_PASSWORD): the janitor's approve step runs as it
	// — inari-server forbids a requester deciding their own decommission, and
	// the suite personas are deliberately not platform-admins. No role
	// assignments, no suite consumption.
	OpsUser     string
	OpsPassword string

	AdminClientSecret string // SEED_KC_ADMIN_CLIENT_SECRET (else read from the inari-keycloak-admin secret)

	HandoffConfigMap string // E2E_HANDOFF_CONFIGMAP "namespace/name" ("" = local mode)
	ToolboxName      string // E2E_TOOLBOX_NAME
}

func loadConfig() (config, error) {
	cfg := config{
		Realm:            "inari",
		HandoffConfigMap: os.Getenv("E2E_HANDOFF_CONFIGMAP"),
		ToolboxName:      envOr("E2E_TOOLBOX_NAME", "e2e-toolbox"),
	}
	cfg.KCURL = strings.TrimRight(os.Getenv("E2E_KC_URL"), "/")
	if cfg.KCURL == "" {
		return cfg, errors.New("E2E_KC_URL is required (Keycloak base URL — browser-reachable == API-reachable on the live env)")
	}
	domain := os.Getenv("E2E_BASE_DOMAIN")
	if domain == "" && cfg.HandoffConfigMap != "" {
		return cfg, errors.New("E2E_BASE_DOMAIN is required in ConfigMap mode (live env)")
	}
	if domain != "" {
		cfg.UIOrigin = "https://" + domain
	} else {
		// Local (kind) mode: explicit legacy origins.
		cfg.UIOrigin = envOr("UI_ORIGIN", "http://127.0.0.1:8080")
	}
	cfg.APIBase = os.Getenv("API_BASE")
	if cfg.APIBase == "" {
		if domain != "" {
			cfg.APIBase = cfg.UIOrigin + "/api/v1"
		} else {
			cfg.APIBase = envOr("API_URL", "http://127.0.0.1:18090/api/v1")
		}
	}
	cfg.TenantName = envOr("E2E_TENANT_NAME", "E2E Org")
	cfg.TenantSlug = envOr("E2E_TENANT_SLUG", envOr("TENANT", "e2e-org"))
	cfg.GitURL = os.Getenv("E2E_GIT_URL")
	cfg.User = envOr("E2E_USER", "dev-admin")
	cfg.Password = envOr("E2E_PASSWORD", "dev-admin")
	cfg.OpsUser = envOr("E2E_OPS_USER", "e2e-ops")
	cfg.OpsPassword = envOr("E2E_OPS_PASSWORD", "e2e-ops")
	cfg.PersonaPasswords = map[string]string{}
	for _, p := range Personas {
		key := "E2E_" + strings.ToUpper(strings.TrimPrefix(p.Username, "e2e-")) + "_PASSWORD"
		cfg.PersonaPasswords[p.Username] = envOr(key, p.Username)
	}
	cfg.AdminClientSecret = strings.TrimSpace(os.Getenv("SEED_KC_ADMIN_CLIENT_SECRET"))
	cfg.Namespace = os.Getenv("E2E_NAMESPACE")
	if cfg.Namespace == "" {
		if ns, _, ok := strings.Cut(cfg.HandoffConfigMap, "/"); ok && ns != "" {
			cfg.Namespace = ns
		} else {
			cfg.Namespace = "inari"
		}
	}
	return cfg, nil
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func logf(format string, args ...any) {
	fmt.Printf("seed: "+format+"\n", args...)
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "seed: FAIL: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	cmMode := cfg.HandoffConfigMap != ""
	logf("seeding against KC=%s API=%s tenant=%s (mode: %s)",
		cfg.KCURL, cfg.APIBase, cfg.TenantSlug, map[bool]string{true: "configmap", false: "local"}[cmMode])

	// Kubernetes store: needed for the handoff/toolbox (CM mode) and as the
	// fallback source of the KC admin client secret.
	var kube kubeStore
	var kubeErr error
	getKube := func() (kubeStore, error) {
		if kube == nil && kubeErr == nil {
			kube, kubeErr = newKubeStore()
		}
		return kube, kubeErr
	}

	kc := NewKC(cfg.KCURL, cfg.Realm)

	adminSecret := cfg.AdminClientSecret
	if adminSecret == "" {
		k, err := getKube()
		if err != nil {
			return fmt.Errorf("SEED_KC_ADMIN_CLIENT_SECRET unset and no Kubernetes access for the secret fallback: %w", err)
		}
		adminSecret, err = k.ReadSecretKey(cfg.Namespace, "inari-keycloak-admin", "client-secret")
		if err != nil {
			return fmt.Errorf("reading inari-keycloak-admin client-secret in ns %s (stack not up?): %w", cfg.Namespace, err)
		}
	}
	at, err := kc.ClientCredentialsToken("inari-platform-admin", adminSecret)
	if err != nil {
		return fmt.Errorf("KC admin token: %w", err)
	}

	// --- A1: inari-ui redirect whitelist (owned here — hard rule §8.4) ----
	if err := ensureClientRedirects(kc, at, cfg.UIOrigin); err != nil {
		return err
	}

	// --- A2: GAP(default-scopes) repair on inari-server + inari-ui --------
	for _, clientID := range []string{"inari-server", "inari-ui"} {
		for _, scope := range []struct{ name, body string }{
			{"basic", basicScope},
			{"organization", organizationScope},
		} {
			attached, err := ensureDefaultScope(kc, at, clientID, scope.name, scope.body)
			if err != nil {
				return fmt.Errorf("ensuring scope %s on %s: %w", scope.name, clientID, err)
			}
			if attached {
				logf("%s: attached default scope %s", clientID, scope.name)
			}
		}
	}

	// --- A3: GAP(kc-groups-mapper) on the inari-server client -------------
	{
		client, err := kc.GetClient(at, "inari-server")
		if err != nil {
			return err
		}
		if client == nil {
			return errors.New("realm-imported inari-server client missing")
		}
		uuid, _ := client["id"].(string)
		var mapper map[string]any
		if err := json.Unmarshal([]byte(groupsMapper), &mapper); err != nil {
			return err
		}
		created, err := kc.EnsureProtocolMapper(at, uuid, "groups", mapper)
		if err != nil {
			return fmt.Errorf("ensuring groups mapper: %w", err)
		}
		if created {
			logf("inari-server: added groups mapper (full group paths)")
		}
	}

	// --- A5: dev-admin final state + platform-admins membership -----------
	devAdminID, err := ensureUser(kc, at, cfg.User, cfg.Password, "Dev", "Admin")
	if err != nil {
		return fmt.Errorf("ensuring dev-admin: %w", err)
	}
	groupID, err := kc.FindGroup(at, "platform-admins")
	if err != nil {
		return err
	}
	if groupID == "" {
		return errors.New("platform-admins group missing (realm import did not run?)")
	}
	if err := kc.AddUserToGroup(at, devAdminID, groupID); err != nil {
		return fmt.Errorf("adding dev-admin to platform-admins: %w", err)
	}

	// --- A6: e2e-ops platform-ops identity (NOT a persona) ----------------
	// The janitor's approve step authenticates as this user: inari-server
	// forbids a requester deciding their own decommission request, and the
	// suite personas are deliberately NOT platform-admins (four-eyes).
	// platform-admins member, no role assignments, no suite consumption.
	opsID, err := ensureUser(kc, at, cfg.OpsUser, cfg.OpsPassword, "E2E", "Ops")
	if err != nil {
		return fmt.Errorf("ensuring %s: %w", cfg.OpsUser, err)
	}
	if err := kc.AddUserToGroup(at, opsID, groupID); err != nil {
		return fmt.Errorf("adding %s to platform-admins: %w", cfg.OpsUser, err)
	}

	// --- A4: token probe (tripwire for the scope repair) ------------------
	if err := probeToken(kc, cfg.User, cfg.Password); err != nil {
		return err
	}

	// --- B1: tenant --------------------------------------------------------
	ut, err := kc.UserToken(cfg.User, cfg.Password)
	if err != nil {
		return fmt.Errorf("dev-admin user token: %w", err)
	}
	api := NewAPI(cfg.APIBase)
	api.SetToken(ut)

	orgKCID, err := ensureTenant(api, cfg.TenantSlug, cfg.TenantName)
	if err != nil {
		return err
	}

	// --- C: personas (KC users + org join) --------------------------------
	userIDs := map[string]string{}
	for _, p := range Personas {
		id, err := ensureUser(kc, at, p.Username, cfg.PersonaPasswords[p.Username], p.Username, "E2E")
		if err != nil {
			return fmt.Errorf("ensuring persona %s: %w", p.Username, err)
		}
		userIDs[p.Username] = id
		if err := kc.JoinOrganization(at, orgKCID, id); err != nil {
			return fmt.Errorf("joining %s to the KC organization: %w", p.Username, err)
		}
		logf("%s joined KC organization %s", p.Username, cfg.TenantSlug)
	}

	// --- C: role assignments via the inari-server API (P0 signal) ---------
	if err := ensureRoleAssignments(api, userIDs, cfg.TenantSlug); err != nil {
		return err
	}

	// --- B2: cluster registration (agent install is env-prep's job) -------
	clusterID, orgID, err := ensureCluster(api, cfg.TenantSlug)
	if err != nil {
		return err
	}

	// --- B3 + B4: default git-config and the git write-path proof ----------
	// The default repo follows the server's own convention for the local
	// provider (bare <slug>-inari-state — INARI_GIT_STATE_REPO_ORG is empty):
	// the tenant-created RBAC-materialization event commits the baseline there
	// automatically, so no synthetic trigger is needed. (The retired .mjs
	// seeded the org-prefixed form, which nothing ever materialized — the
	// kind hostPath never surfaced that.)
	defaultRepo := cfg.TenantSlug + "-inari-state"
	repoInForce, err := ensureGitConfig(api, cfg.TenantSlug, defaultRepo)
	if err != nil {
		return err
	}
	if cfg.GitURL != "" {
		repoURL := strings.TrimRight(cfg.GitURL, "/") + "/" + repoInForce + ".git"
		if err := waitGitServed(repoURL, 180*time.Second, 5*time.Second); err != nil {
			return fmt.Errorf("git write-path proof failed for %s: %w "+
				"(the tenant-created RBAC materialization never landed — check the rbacmaterialize consumer)",
				repoURL, err)
		}
		logf("git service serves %s", repoURL)
	}

	// --- D: handoff publish (ConfigMap mode only) ---------------------------
	if cmMode {
		k, err := getKube()
		if err != nil {
			return fmt.Errorf("handoff publish needs Kubernetes access: %w", err)
		}
		if err := k.EnsureToolbox(cfg.Namespace, cfg.ToolboxName, "curlimages/curl:8.10.1"); err != nil {
			return fmt.Errorf("ensuring toolbox pod: %w", err)
		}
		ns, name, ok := strings.Cut(cfg.HandoffConfigMap, "/")
		if !ok || ns == "" || name == "" {
			return fmt.Errorf("E2E_HANDOFF_CONFIGMAP must be \"namespace/name\", got %q", cfg.HandoffConfigMap)
		}
		data := map[string]string{
			"namespace":  cfg.Namespace,
			"toolbox":    cfg.ToolboxName,
			"tenant":     cfg.TenantSlug,
			"kc_uid":     devAdminID,
			"org_kc_id":  orgKCID,
			"cluster_id": clusterID,
			"org_id":     orgID,
		}
		if err := k.WriteConfigMap(ns, name, data); err != nil {
			return fmt.Errorf("writing handoff ConfigMap %s: %w", cfg.HandoffConfigMap, err)
		}
		logf("handoff ConfigMap %s: tenant=%s cluster=%s org_kc_id=%s", cfg.HandoffConfigMap, cfg.TenantSlug, clusterID, orgKCID)
	}

	logf("OK — all personas seeded")
	return nil
}

// ensureClientRedirects re-merges the browser origin into the inari-ui
// client's redirect whitelist (A1).
func ensureClientRedirects(kc *KC, at, uiOrigin string) error {
	client, err := kc.GetClient(at, "inari-ui")
	if err != nil {
		return err
	}
	if client == nil {
		return errors.New("inari-ui client not found (realm import did not run?)")
	}
	body, changed := mergeClientRedirects(client, uiOrigin)
	if !changed {
		logf("inari-ui redirect whitelist already present — skipped")
		return nil
	}
	id, _ := client["id"].(string)
	if err := kc.PutClient(at, id, body); err != nil {
		return fmt.Errorf("updating inari-ui client: %w", err)
	}
	logf("inari-ui: whitelisted %s (redirectUris + webOrigins)", uiOrigin)
	return nil
}

// ensureUser creates the user when missing and always converges the final
// state (KC 26: create alone can leave the account unverified / with
// required actions; PUT /users/{id} is a FULL replace, so merge onto the
// stored user to keep the declarative-profile attributes). The password is
// reset unconditionally so Vault-side rotations converge on the next run.
// Never touches groups: personas must NOT join platform-admins.
func ensureUser(kc *KC, at, username, password, firstName, lastName string) (string, error) {
	id, err := kc.FindUser(at, username)
	if err != nil {
		return "", err
	}
	if id == "" {
		id, err = kc.CreateUser(at, map[string]any{
			"username":      username,
			"enabled":       true,
			"email":         username + "@inari.local",
			"firstName":     firstName,
			"lastName":      lastName,
			"emailVerified": true,
			"credentials":   []map[string]any{{"type": "password", "value": password, "temporary": false}},
		})
		if err != nil {
			return "", err
		}
		logf("created KC user %s", username)
	}
	stored, err := kc.GetUser(at, username)
	if err != nil {
		return "", err
	}
	if stored == nil {
		return "", fmt.Errorf("created KC user %s but cannot read it back", username)
	}
	stored["emailVerified"] = true
	stored["requiredActions"] = []string{}
	stored["enabled"] = true
	// KC 26's declarative user profile marks firstName/lastName required;
	// the realm import ships dev-admin without them and every grant dies
	// with "Account is not fully set up". Fill only when absent.
	if stored["firstName"] == nil || stored["firstName"] == "" {
		stored["firstName"] = firstName
	}
	if stored["lastName"] == nil || stored["lastName"] == "" {
		stored["lastName"] = lastName
	}
	if err := kc.PutUser(at, id, stored); err != nil {
		return "", fmt.Errorf("converging user %s: %w", username, err)
	}
	if err := kc.ResetPassword(at, id, password); err != nil {
		return "", fmt.Errorf("resetting password for %s: %w", username, err)
	}
	return id, nil
}

// probeToken mints a dev-admin token and requires the claims the server
// validates: sub (basic scope) and aud containing inari-server. This is the
// tripwire that would have caught the missing-scope realm state in seconds.
func probeToken(kc *KC, username, password string) error {
	token, err := kc.UserToken(username, password)
	if err != nil {
		return fmt.Errorf("probe token request as %s: %w", username, err)
	}
	claims, err := jwtClaims(token)
	if err != nil {
		return fmt.Errorf("decoding probe token: %w", err)
	}
	if _, ok := claims["sub"]; !ok {
		return errors.New("probe token has no sub claim (basic scope missing — GAP(default-scopes) repair broken)")
	}
	if !audContains(claims["aud"], "inari-server") {
		return fmt.Errorf("probe token has wrong aud (audience mapper missing): %v", claims["aud"])
	}
	return nil
}

// jwtClaims decodes the payload of a JWT without verifying the signature
// (the seed only inspects claims of a token it minted itself).
func jwtClaims(token string) (map[string]any, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, fmt.Errorf("not a JWT (%d parts)", len(parts))
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, err
	}
	var claims map[string]any
	return claims, json.Unmarshal(raw, &claims)
}

// audContains: scalar equal or array containing the want value.
func audContains(aud any, want string) bool {
	switch v := aud.(type) {
	case string:
		return v == want
	case []any:
		for _, a := range v {
			if a == want {
				return true
			}
		}
	}
	return false
}

// ensureTenant returns the tenant's Keycloak organization id, creating the
// tenant when absent. The bounded poll covers token/Keycloak warm-up and
// the PlatformGroupSync lag before org_creator tuples materialize (the
// outcome is an API response, not a k8s condition).
func ensureTenant(api *API, slug, displayName string) (string, error) {
	tenants, err := api.ListTenants()
	if err != nil {
		return "", fmt.Errorf("listing tenants: %w", err)
	}
	for _, t := range tenants {
		if t.Slug == slug {
			if t.KeycloakOrgID == "" {
				return "", fmt.Errorf("tenant %q exists but carries no keycloakOrgId", slug)
			}
			logf("tenant %s exists — skipped create", slug)
			return t.KeycloakOrgID, nil
		}
	}
	logf("creating tenant %q", slug)
	var kcOrgID string
	err = poll(120*time.Second, 10*time.Second, func() error {
		id, err := api.CreateTenant(slug, displayName)
		if err != nil || id == "" {
			return fmt.Errorf("transient: %v", err) // warm-up / FGA tuple lag: keep polling
		}
		kcOrgID = id
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("tenant creation (organization.keycloakOrgId): %w", err)
	}
	return kcOrgID, nil
}

// ensureCluster returns the e2e-self cluster id and agent org id,
// registering the cluster when absent. Bounded polls cover asynchronous
// authz materialization: on ADR-0014 servers the FGA tuples granting
// clusters.register are written by the outbox relay AFTER tenant creation
// returns, so an immediate register 403s.
func ensureCluster(api *API, tenant string) (clusterID, orgID string, err error) {
	clusters, err := api.ListClusters(tenant)
	if err != nil {
		return "", "", fmt.Errorf("listing clusters: %w", err)
	}
	for _, c := range clusters {
		if c.Name == "e2e-self" {
			logf("cluster e2e-self exists — skipped registration")
			return c.ID, c.OrgID, nil
		}
	}
	logf("registering cluster e2e-self")
	err = poll(120*time.Second, 5*time.Second, func() error {
		id, org, err := api.RegisterCluster(tenant, "e2e-self", map[string]string{"e2e": "true"})
		if err != nil || id == "" {
			return fmt.Errorf("transient: %v", err) // authz tuples not materialized yet: keep polling
		}
		clusterID, orgID = id, org
		return nil
	})
	if err != nil {
		return "", "", fmt.Errorf("cluster registration (clusters_register FGA tuple): %w", err)
	}
	return clusterID, orgID, nil
}

// ensureRoleAssignments grants the persona roles through the real
// membership API. Bounded poll: PUT returns 404 until the server's users
// projection has observed the KC org join (OrgTeamSync interval). Any other
// error fails fast — a broken membership API is a P0-class signal.
func ensureRoleAssignments(api *API, userIDs map[string]string, tenant string) error {
	roles, err := api.ListRoles(tenant)
	if err != nil {
		return fmt.Errorf("GET /tenants/%s/roles failed: %w", tenant, err)
	}
	roleIDs, err := requireBuiltinRoles(roles)
	if err != nil {
		return err
	}

	return poll(120*time.Second, 5*time.Second, func() error {
		members, err := api.ListMembers(tenant)
		if err != nil {
			return fmt.Errorf("GET /tenants/%s/members failed: %w", tenant, err)
		}
		plan := planRoleAssignments(Personas, userIDs, members)
		for _, s := range plan.Skipped {
			logf("%s: role already assigned — skipped", s)
		}
		if plan.MemberLeak != nil {
			logf("WARNING: e2e-member holds roles %v — a spec leaked a role assignment; the role-lifecycle spec must restore e2e-member to role-less", plan.MemberLeak)
		}
		for _, a := range plan.Assignments {
			if err := api.AssignRole(tenant, a.UserID, roleIDs[a.Role]); err != nil {
				var se *StatusError
				if errors.As(err, &se) && se.Status == 404 {
					return err // projection lag — keep polling until the deadline
				}
				return &fatalError{fmt.Errorf("PUT /tenants/%s/members/%s (%s, role %s) failed: %w", tenant, a.UserID, a.Username, a.Role, err)}
			}
			logf("%s: assigned role %s via PUT /tenants/%s/members/%s", a.Username, a.Role, tenant, a.UserID)
		}
		return nil
	})
}

// ensureGitConfig seeds a stable default exactly once (the server 404s
// until the first write and there is no DELETE; the git-config spec saves
// and restores the ORIGINAL repo, so a config must exist before it runs).
// Returns the repo currently in force (the seeded default, or whatever a
// previous run/environment set).
func ensureGitConfig(api *API, tenant, defaultRepo string) (string, error) {
	cfg, err := api.GetGitConfig(tenant)
	if err != nil {
		return "", err
	}
	if cfg != nil {
		logf("git-config for %s already set (%s) — skipped", tenant, cfg.Repo)
		return cfg.Repo, nil
	}
	if err := api.PutGitConfig(tenant, defaultRepo); err != nil {
		return "", fmt.Errorf("seeding git-config: %w", err)
	}
	logf("git-config for %s: seeded %s (direct)", tenant, defaultRepo)
	return defaultRepo, nil
}

// fatalError marks a poll callback error as final (no retry).
type fatalError struct{ err error }

func (e *fatalError) Error() string { return e.err.Error() }
func (e *fatalError) Unwrap() error { return e.err }

// poll retries fn every interval until it succeeds or the timeout passes.
// A *fatalError aborts immediately.
func poll(timeout, interval time.Duration, fn func() error) error {
	deadline := time.Now().Add(timeout)
	var last error
	for {
		err := fn()
		if err == nil {
			return nil
		}
		var fatal *fatalError
		if errors.As(err, &fatal) {
			return fatal.err
		}
		last = err
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out after %s: %w", timeout, last)
		}
		time.Sleep(interval)
	}
}
