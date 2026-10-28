//go:build e2e

package suite

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"7k-inari/inari-release-bundle/scripts/e2e/stack/internal/kc"
	"7k-inari/inari-release-bundle/scripts/e2e/stack/internal/kube"
	"7k-inari/inari-release-bundle/scripts/e2e/stack/internal/poll"
)

// Cluster asserts registration aftermath: agent connect (cluster state
// active), ESO-projected OIDC client secret, capability stream, heartbeat
// freshness. The register/token calls and the agent helm install are
// provisioning (golden-path.sh) — these are the observations.
func Cluster(t *testing.T, e *Env) {
	// Cluster state advances via the agent gRPC stream (app-level); no k8s
	// condition exists for it.
	poll.Eventually(t, 120*time.Second, 5*time.Second, func() (bool, error) {
		cl, err := e.API.GetCluster(e.ClusterID)
		if err != nil {
			return false, nil // transient API/KC warm-up: keep polling
		}
		return cl.State == "active", nil
	}, "cluster active (agent connected; logs: kubectl -n inari-system logs deploy/inari-agent)")

	// ESO sets Ready=True on the ExternalSecret once the Vault read +
	// projection succeeds — an event-driven wait, not a blind poll.
	require.NoError(t, kube.WaitCondition("inari-system", "condition=Ready",
		"120s", "externalsecret/inari-agent-oidc-client"),
		"ESO never projected inari-agent-oidc-client")
	secB64, err := kube.Kubectl("-n", "inari-system", "get", "secret",
		"inari-agent-oidc-client", "-o", "jsonpath={.data.client-secret}")
	require.NoError(t, err, "ExternalSecret Ready but secret missing")
	esoSecret, err := base64.StdEncoding.DecodeString(strings.TrimSpace(secB64))
	require.NoError(t, err)

	cl, err := e.API.GetCluster(e.ClusterID)
	require.NoError(t, err)
	require.NotEmpty(t, cl.KeycloakClientID)
	adminTok, err := e.KC.AdminToken()
	require.NoError(t, err)
	kcSecret, err := e.KC.ClientSecret(adminTok, cl.KeycloakClientID)
	require.NoError(t, err)
	assert.Equal(t, kcSecret, string(esoSecret),
		"ESO-projected secret must match the Keycloak client secret")

	// Capabilities arrive over the agent stream (app-level SSE).
	var caps int
	poll.Eventually(t, 180*time.Second, 5*time.Second, func() (bool, error) {
		caps, err = e.API.CapabilitiesLength(e.ClusterID)
		if err != nil {
			return false, nil
		}
		return caps > 0, nil
	}, "capabilities streamed (logs: kubectl -n inari-system logs deploy/inari-agent)")
	t.Logf("capabilities streamed: %d", caps)

	// Heartbeat freshness: lastSeenAt must advance (throttled to 15s in the
	// agentgateway). Poll, don't sleep: a stream reconnect mid-window
	// legitimately stalls the next beat past a fixed 30s sleep.
	cl1, err := e.API.GetCluster(e.ClusterID)
	require.NoError(t, err)
	poll.Eventually(t, 120*time.Second, 5*time.Second, func() (bool, error) {
		cl2, err := e.API.GetCluster(e.ClusterID)
		if err != nil {
			return false, nil
		}
		return cl2.LastSeenAt != cl1.LastSeenAt, nil
	}, "heartbeat advancing (lastSeenAt changed; logs: kubectl -n inari-system logs deploy/inari-agent)")
}

// stateRepo is the materialized tenant state repo on the host (mounted into
// the kind node by the script; INARI_GIT_PROVIDER=local).
func (e *Env) stateRepo() string {
	return filepath.Join(e.GitHostDir, e.Tenant+"-inari-state.git")
}

// gitShow returns a file from the state repo's main branch.
func gitShow(repo, path string) (string, error) {
	return kube.Run("git", "-C", repo, "show", "main:"+path)
}

// syncRBAC clones the state repo and applies baseline/rbac/, then prunes
// managed bindings absent from the desired set (emulating ArgoCD's prune —
// a mapping flip renders a NEW role-qualified binding because roleRef is
// immutable, so the stale object must be pruned to converge).
// GAP(rbac-e2e-argocd): stands in for the tenant-local ArgoCD, which the
// e2e platform stack does not install.
func syncRBAC(t *testing.T, e *Env) {
	t.Helper()
	work := filepath.Join(e.GitHostDir, "work")
	require.NoError(t, os.RemoveAll(work))
	_, err := kube.Run("git", "clone", "-q", e.stateRepo(), work)
	require.NoError(t, err)
	_, err = kube.Kubectl("apply", "-f", filepath.Join(work, "baseline", "rbac"))
	require.NoError(t, err)

	list, err := kube.Kubectl("get", "clusterrolebinding",
		"-l", "inari.io/tenant="+e.Tenant, "-o", "name")
	require.NoError(t, err)
	desired, err := os.ReadFile(filepath.Join(work, "baseline", "rbac", "clusterrolebindings.yaml"))
	require.NoError(t, err)
	for _, b := range strings.Fields(list) {
		name := strings.TrimPrefix(b, "clusterrolebinding.rbac.authorization.k8s.io/")
		if !strings.Contains(string(desired), "  name: "+name+"\n") {
			_, err := kube.Kubectl("delete", "clusterrolebinding", name)
			require.NoError(t, err, "pruning stale binding %s", name)
		}
	}
}

// RBACMaterialization asserts the RBAC mapping materialization chain
// (plan §7.1): tenant.created fired the materializer; the state repo gains
// baseline/rbac/*; applied to the cluster, group membership maps to real
// RBAC; a mapping flip converges (new binding, stale pruned).
func RBACMaterialization(t *testing.T, e *Env) {
	repo := e.stateRepo()
	poll.Eventually(t, 180*time.Second, 5*time.Second, func() (bool, error) {
		_, err := gitShow(repo, "baseline/rbac/clusterroles.yaml")
		return err == nil, nil
	}, "state repo materialized (server logs: kubectl -n "+e.Namespace+" logs deploy/inari-server)")

	syncRBAC(t, e)
	for _, role := range []string{"admin", "operator", "editor", "viewer"} {
		_, err := kube.Kubectl("get", "clusterrole", "tenant-"+e.Tenant+"-"+role)
		require.NoError(t, err, "clusterrole tenant-%s-%s missing after sync", e.Tenant, role)
	}
	ref, err := kube.Kubectl("get", "clusterrolebinding",
		"tenant-"+e.Tenant+"-viewers-viewer", "-o", "jsonpath={.roleRef.name}")
	require.NoError(t, err)
	assert.Equal(t, "tenant-"+e.Tenant+"-viewer", ref)

	// kubelogin-style check: the rbac-viewer user (seeded by the script into
	// the viewers team group; GAP(kc-groups-mapper) ensures the groups claim
	// carries full group paths) must read but not write.
	viewerTok, err := e.KC.PasswordToken("rbac-viewer", "rbac-viewer", "openid")
	require.NoError(t, err, "rbac-viewer token request failed")
	claims, err := kc.Claims(viewerTok)
	require.NoError(t, err)
	assert.Contains(t, claims["groups"], "/tenant-"+e.Tenant+"/viewers",
		"viewer token lacks the groups claim entry (GAP(kc-groups-mapper) mapper missing?)")

	viewerGroup := []string{"/tenant-" + e.Tenant + "/viewers"}
	ok, err := kube.AuthCanI("oidc:rbac-viewer", viewerGroup, "get", "pods")
	require.NoError(t, err)
	assert.True(t, ok, "viewer group cannot get pods (binding not effective)")
	// GAP(rbac-e2e-jwt-authn): this asserts the authorizer decision, not real
	// JWT authn — wiring kind's kube-apiserver AuthenticationConfiguration
	// to Keycloak is a follow-up.
	ok, err = kube.AuthCanI("oidc:rbac-viewer", viewerGroup, "create", "deployments")
	require.NoError(t, err)
	assert.False(t, ok, "viewer group can create deployments (viewer role over-privileged)")

	// Flip the viewers team mapping to editor: outbox → rbacmaterialize →
	// git push; observable only in the state repo, so poll then re-sync.
	require.NoError(t, e.API.PutRBACMappings("viewers", "editor"))
	poll.Eventually(t, 180*time.Second, 5*time.Second, func() (bool, error) {
		bindings, err := gitShow(repo, "baseline/rbac/clusterrolebindings.yaml")
		if err != nil {
			return false, nil
		}
		return strings.Contains(bindings, "  name: tenant-"+e.Tenant+"-viewers-editor\n"), nil
	}, "state repo binding updated after the mapping change")
	syncRBAC(t, e)

	ref, err = kube.Kubectl("get", "clusterrolebinding",
		"tenant-"+e.Tenant+"-viewers-editor", "-o", "jsonpath={.roleRef.name}")
	require.NoError(t, err)
	assert.Equal(t, "tenant-"+e.Tenant+"-editor", ref,
		"viewers binding did not converge to tenant-"+e.Tenant+"-editor")
	_, err = kube.Kubectl("get", "clusterrolebinding", "tenant-"+e.Tenant+"-viewers-viewer")
	assert.Error(t, err, "stale viewers-viewer binding not pruned after the mapping change")
	ok, err = kube.AuthCanI("oidc:rbac-viewer", viewerGroup, "create", "deployments")
	require.NoError(t, err)
	assert.True(t, ok, "editor-mapped group cannot create deployments after the mapping change")
}
