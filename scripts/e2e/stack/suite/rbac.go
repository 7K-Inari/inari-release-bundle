//go:build e2e

package suite

import (
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"7k-inari/inari-release-bundle/scripts/e2e/stack/internal/kc"
	"7k-inari/inari-release-bundle/scripts/e2e/stack/internal/kube"
	"7k-inari/inari-release-bundle/scripts/e2e/stack/internal/poll"
)

// RBAC materialization phase helpers (plan §7.1): tenant.created fired the
// materializer; the state repo gains baseline/rbac/*; applied to the
// cluster, group membership maps to real RBAC; a mapping flip converges
// (new binding, stale pruned).

// stateRepo is the materialized tenant state repo on the host (mounted into
// the kind node by the provisioner; INARI_GIT_PROVIDER=local).
func (e *Env) stateRepo() string {
	return filepath.Join(e.GitHostDir, e.Tenant+"-inari-state.git")
}

// gitShow returns a file from the state repo's main branch.
func gitShow(repo, path string) (string, error) {
	return kube.Run("git", "-C", repo, "show", "main:"+path)
}

// WaitStateRepoMaterialized polls until tenant.created has materialized
// baseline/rbac/* into the tenant state repo.
func WaitStateRepoMaterialized(t TB, e *Env) {
	t.Helper()
	repo := e.stateRepo()
	poll.Eventually(t, 180*time.Second, 5*time.Second, func() (bool, error) {
		_, err := gitShow(repo, "baseline/rbac/clusterroles.yaml")
		return err == nil, nil
	}, "state repo materialized (server logs: kubectl -n "+e.Namespace+" logs deploy/inari-server)")
}

// SyncRBAC clones the state repo and applies baseline/rbac/, then prunes
// managed bindings absent from the desired set (emulating ArgoCD's prune —
// a mapping flip renders a NEW role-qualified binding because roleRef is
// immutable, so the stale object must be pruned to converge).
// GAP(rbac-e2e-argocd): stands in for the tenant-local ArgoCD, which the
// e2e platform stack does not install.
func SyncRBAC(t TB, e *Env) {
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

// AssertBaselineRBACEffective checks the applied baseline set: all four
// clusterroles exist, the viewers binding points at the viewer role, and the
// rbac-viewer persona (seeded into the viewers team group) can read but not
// write.
func AssertBaselineRBACEffective(t TB, e *Env) {
	t.Helper()
	for _, role := range []string{"admin", "operator", "editor", "viewer"} {
		_, err := kube.Kubectl("get", "clusterrole", "tenant-"+e.Tenant+"-"+role)
		require.NoError(t, err, "clusterrole tenant-%s-%s missing after sync", e.Tenant, role)
	}
	ref, err := kube.Kubectl("get", "clusterrolebinding",
		"tenant-"+e.Tenant+"-viewers-viewer", "-o", "jsonpath={.roleRef.name}")
	require.NoError(t, err)
	assert.Equal(t, "tenant-"+e.Tenant+"-viewer", ref)

	// kubelogin-style check: the rbac-viewer user (seeded by the provisioner
	// into the viewers team group; GAP(kc-groups-mapper) ensures the groups
	// claim carries full group paths) must read but not write.
	viewerTok, err := e.KC.PasswordToken("rbac-viewer", "rbac-viewer", "openid")
	require.NoError(t, err, "rbac-viewer token request failed")
	claims, err := kc.Claims(viewerTok)
	require.NoError(t, err)
	assert.Contains(t, claims["groups"], "/tenant-"+e.Tenant+"/viewers",
		"viewer token lacks the groups claim entry (GAP(kc-groups-mapper) mapper missing?)")

	ok, err := kube.AuthCanI("oidc:rbac-viewer", viewerGroup(e), "get", "pods")
	require.NoError(t, err)
	assert.True(t, ok, "viewer group cannot get pods (binding not effective)")
	// GAP(rbac-e2e-jwt-authn): this asserts the authorizer decision, not real
	// JWT authn — wiring kind's kube-apiserver AuthenticationConfiguration
	// to Keycloak is a follow-up.
	ok, err = kube.AuthCanI("oidc:rbac-viewer", viewerGroup(e), "create", "deployments")
	require.NoError(t, err)
	assert.False(t, ok, "viewer group can create deployments (viewer role over-privileged)")
}

func viewerGroup(e *Env) []string {
	return []string{"/tenant-" + e.Tenant + "/viewers"}
}

// FlipRBACMapping PUTs a team→role mapping and polls until the outbox →
// rbacmaterialize → git push chain renders the new role-qualified binding in
// the state repo. Observable only in the state repo, so poll then re-sync.
func FlipRBACMapping(t TB, e *Env, team, role string) {
	t.Helper()
	require.NoError(t, e.API.PutRBACMappings(team, role))
	repo := e.stateRepo()
	poll.Eventually(t, 180*time.Second, 5*time.Second, func() (bool, error) {
		bindings, err := gitShow(repo, "baseline/rbac/clusterrolebindings.yaml")
		if err != nil {
			return false, nil
		}
		return strings.Contains(bindings, "  name: tenant-"+e.Tenant+"-"+team+"-"+role+"\n"), nil
	}, "state repo binding updated after the "+team+"→"+role+" mapping change")
}

// AssertMappingFlipConverged checks the post-flip state after SyncRBAC: the
// new role-qualified binding points at the new role, the stale binding was
// pruned, and the re-mapped group gained the new role's permissions.
func AssertMappingFlipConverged(t TB, e *Env, team, role string) {
	t.Helper()
	ref, err := kube.Kubectl("get", "clusterrolebinding",
		"tenant-"+e.Tenant+"-"+team+"-"+role, "-o", "jsonpath={.roleRef.name}")
	require.NoError(t, err)
	assert.Equal(t, "tenant-"+e.Tenant+"-"+role, ref,
		"%s binding did not converge to tenant-%s-%s", team, e.Tenant, role)
	// The flip's previous mapping is hard-coded in the specs (viewers→viewer
	// baseline, flipped to editor): the stale binding must be pruned.
	_, err = kube.Kubectl("get", "clusterrolebinding", "tenant-"+e.Tenant+"-"+team+"-viewer")
	assert.Error(t, err, "stale %s-viewer binding not pruned after the mapping change", team)
	ok, err := kube.AuthCanI("oidc:rbac-viewer", viewerGroup(e), "create", "deployments")
	require.NoError(t, err)
	assert.True(t, ok, "%s-mapped group cannot create deployments after the mapping change", role)
}
