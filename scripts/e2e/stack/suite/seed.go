//go:build e2e

// Keycloak realm/client/user seeding + tenant/cluster registration — phase 3
// of the shell→Go migration, ported step-for-step from golden-path.sh. All
// bodies stay literal JSON so the GAP shapes remain byte-identical; every
// workaround keeps its GAP marker (see env.go for the tracked upstream
// fixes). Runs against the already-installed stack (issuer converged,
// toolbox up) and ends with the agent installed and every handoff field
// known.
package suite

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"7k-inari/inari-release-bundle/scripts/e2e/stack/internal/kc"
	"7k-inari/inari-release-bundle/scripts/e2e/stack/internal/poll"
)

// Literal seeding bodies (golden-path.sh parity).

const devAdminCreate = `{"username":"dev-admin","enabled":true,"email":"dev-admin@inari.local","emailVerified":true,"firstName":"Dev","lastName":"Admin","credentials":[{"type":"password","value":"dev-admin","temporary":false}]}`
const devAdminUpdate = `{"emailVerified":true,"firstName":"Dev","lastName":"Admin","requiredActions":[],"enabled":true}`

const inariServerClientCreate = `{"clientId":"inari-server","enabled":true,"publicClient":true,"standardFlowEnabled":true,"directAccessGrantsEnabled":true,"redirectUris":["http://localhost/*"],"webOrigins":["+"],"defaultClientScopes":["openid","profile","email","organization"]}`

// GAP(aud-mapper): the server validates aud=inari-server.
const audienceMapper = `{"name":"audience-inari-server","protocol":"openid-connect","protocolMapper":"oidc-audience-mapper","config":{"included.client.audience":"inari-server","id.token.claim":"false","access.token.claim":"true","userinfo.token.claim":"false"}}`

// GAP(default-scopes): when the realm import JSON carries an explicit
// clientScopes array, Keycloak skips creating its built-in default scopes
// (basic, organization, ...) and every token request dies with
// invalid_scope. Recreated with the built-in mapper shapes.
//
// Without basic, tokens carry no sub claim (KC 26 user-profile model).
const basicScope = `{"name":"basic","protocol":"openid-connect","attributes":{"include.in.token.scope":"false","display.on.consent.screen":"false"},"protocolMappers":[{"name":"sub","protocol":"openid-connect","protocolMapper":"oidc-sub-mapper","config":{"access.token.claim":"true","id.token.claim":"true"}}]}`

// Without organization, scope="openid organization:*" is rejected.
const organizationScope = `{"name":"organization","protocol":"openid-connect","attributes":{"include.in.token.scope":"true","display.on.consent.screen":"false"},"protocolMappers":[{"name":"organization","protocol":"openid-connect","protocolMapper":"oidc-organization-membership-mapper","config":{"id.token.claim":"true","access.token.claim":"true","claim.name":"organization","jsonType.label":"String","multivalued":"true"}}]}`

// GAP(kc-groups-mapper): the groups claim carries full group paths.
const groupsMapper = `{"name":"groups","protocol":"openid-connect","protocolMapper":"oidc-group-membership-mapper","config":{"claim.name":"groups","full.path":"true","id.token.claim":"true","access.token.claim":"true","userinfo.token.claim":"true"}}`

const rbacViewerCreate = `{"username":"rbac-viewer","enabled":true,"emailVerified":true,"credentials":[{"type":"password","value":"rbac-viewer","temporary":false}]}`
const rbacViewerUpdate = `{"email":"rbac-viewer@inari.local","emailVerified":true,"firstName":"RBAC","lastName":"Viewer","requiredActions":[],"enabled":true}`

// seedResult carries the identities seeding establishes (handoff fields).
type seedResult struct {
	KCUID   string // dev-admin Keycloak user id
	OrgKCID string // tenant org's Keycloak organization id
}

// seedKeycloakAndTenant performs the KC realm/user seeding, tenant creation
// and cluster registration, and installs the agent. Requires the chart
// stack (provisionCharts) to have completed.
func seedKeycloakAndTenant(t *testing.T, e *Env, c *ProvisionConfig) (seedResult, string, string) {
	t.Helper()
	var res seedResult

	at, err := e.KC.AdminToken()
	require.NoError(t, err)

	// GAP(kc-realm): ensure dev user + public client with correct scopes.
	logf("GAP(kc-realm): ensuring dev user + public client with correct scopes")
	res.KCUID, err = e.KC.EnsureUser(at, "dev-admin", devAdminCreate, devAdminUpdate)
	require.NoError(t, err)
	kcClient, err := e.KC.EnsureClient(at, "inari-server", inariServerClientCreate)
	require.NoError(t, err)

	// GAP(aud-mapper): the script retried the create three times (Keycloak
	// occasionally 409s a fresh client) — same bounded retry here.
	require.Eventually(t, func() bool {
		return e.KC.EnsureMapper(at, kcClient, "audience-inari-server", audienceMapper) == nil
	}, 30*time.Second, 2*time.Second, "audience-inari-server mapper never stuck")

	// GAP(default-scopes): recreate the built-in scopes the realm import
	// suppressed and attach them to the client (idempotent).
	require.NoError(t, e.KC.EnsureClientScope(at, "basic", basicScope, kcClient))
	require.NoError(t, e.KC.EnsureClientScope(at, "organization", organizationScope, kcClient))

	// Sanity: a token must carry sub and aud=inari-server before we proceed.
	probe, err := e.KC.UserToken()
	require.NoError(t, err)
	claims, err := kc.Claims(probe)
	require.NoError(t, err)
	require.NotNil(t, claims["sub"], "token has no sub claim (basic scope missing)")
	require.True(t, audContains(claims["aud"], "inari-server"),
		"token has wrong aud (audience mapper missing): %v", claims["aud"])

	// GAP(kc-platform-group): dev-admin joins platform-admins (drives
	// org_creator tuple sync). Join is idempotent (204).
	logf("GAP(kc-platform-group): ensuring dev-admin is in platform-admins")
	groupID, err := e.KC.EnsureGroup(at, "platform-admins")
	require.NoError(t, err)
	require.NoError(t, e.KC.AddUserToGroup(at, res.KCUID, groupID),
		"failed to add dev-admin to platform-admins")

	// Tenant creation: bounded poll covers transient token/Keycloak warm-up
	// (the outcome is an API response, not a k8s condition).
	logf("creating tenant %q", e.Tenant)
	poll.Eventually(t, 120*time.Second, 10*time.Second, func() (bool, error) {
		id, err := e.API.CreateTenant(e.Tenant, "E2E Org")
		if err != nil || id == "" {
			return false, nil // transient warm-up: keep polling
		}
		res.OrgKCID = id
		return true, nil
	}, "tenant creation (organization.keycloakOrgId)")

	// Register the cluster and issue the agent registration token. Bounded
	// polls cover asynchronous authz materialization: on ADR-0014 servers
	// the FGA tuples granting clusters.register (tenant teams) and the
	// per-cluster relations are written by the outbox relay + tuple-writer
	// consumer AFTER the create call returns, so an immediate follow-up
	// call 403s. Pre-ADR-0014 servers wrote tuples synchronously; the poll
	// is a no-op there.
	logf("registering cluster + issuing token")
	var clusterID, orgID string
	poll.Eventually(t, 120*time.Second, 5*time.Second, func() (bool, error) {
		id, org, err := e.API.RegisterCluster("e2e-self", map[string]string{"e2e": "true"})
		if err != nil || id == "" {
			return false, nil // authz tuples not materialized yet: keep polling
		}
		clusterID, orgID = id, org
		return true, nil
	}, "cluster registration (clusters_register FGA tuple)")
	var regToken string
	poll.Eventually(t, 120*time.Second, 5*time.Second, func() (bool, error) {
		tok, err := e.API.IssueClusterToken(clusterID)
		if err != nil || tok == "" {
			return false, nil // per-cluster tuples not materialized yet
		}
		regToken = tok
		return true, nil
	}, "cluster token issue (per-cluster FGA tuples)")

	logf("installing agent via the inari-agent Helm chart")
	provisionAgent(t, AgentInput{
		Namespace:     e.Namespace,
		ClusterID:     clusterID,
		OrgID:         orgID,
		RegToken:      regToken,
		AgentChartDir: c.AgentChartDir,
		AgentImage:    c.AgentImage,
		VaultDevToken: c.VaultDevToken,
	})

	// GAP(kc-groups-mapper): full group paths in the groups claim.
	logf("GAP(kc-groups-mapper): ensuring the groups claim carries full group paths")
	at, err = e.KC.AdminToken()
	require.NoError(t, err)
	require.NoError(t, e.KC.EnsureMapper(at, kcClient, "groups", groupsMapper),
		"failed to add the groups mapper")

	// kubelogin-style check: a user in the viewers team group must read but
	// not write. The tenant-<slug>/viewers group is created by the server's
	// org sync, so it may lag tenant creation — poll for it.
	logf("ensuring the rbac-viewer Keycloak user + viewers team group membership")
	viewerUID, err := e.KC.EnsureUser(at, "rbac-viewer", rbacViewerCreate, rbacViewerUpdate)
	require.NoError(t, err)
	var viewersGrp string
	poll.Eventually(t, 90*time.Second, 5*time.Second, func() (bool, error) {
		viewersGrp, err = e.KC.GroupByPath(at, "tenant-"+e.Tenant+"/viewers")
		if err != nil || viewersGrp == "" {
			return false, nil // org sync lag: keep polling
		}
		return true, nil
	}, "Keycloak group tenant-"+e.Tenant+"/viewers (tenant seeding)")
	// The script tolerated a join failure here (|| true) — same: membership
	// is asserted downstream by the RBAC phases, which retry via poll.
	_ = e.KC.AddUserToGroup(at, viewerUID, viewersGrp)

	return res, clusterID, orgID
}

// audContains mirrors the script's jq aud check: scalar equal or array
// containing the want value.
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
