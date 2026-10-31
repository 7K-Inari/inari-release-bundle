//go:build e2e

package suite

import (
	"encoding/base64"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"7k-inari/inari-release-bundle/scripts/e2e/stack/internal/helm"
	"7k-inari/inari-release-bundle/scripts/e2e/stack/internal/kind"
	"7k-inari/inari-release-bundle/scripts/e2e/stack/internal/kube"
	"7k-inari/inari-release-bundle/scripts/e2e/stack/internal/poll"
)

// Render-path validation (M5W2 + M5W3-2): proves the new render path
// end-to-end on the golden-path stack — server-rendered native
// AppProject/DNSEndpoint + KRO tenantnamespace instance + provider-keycloak
// TenantRealm/TenantClient XRs sync Healthy via ArgoCD, the RGD teardown
// cascades, Keycloak realms/clients are created (and, for a disposable
// realm, deleted) by provider-keycloak, client secrets land in the tenant
// namespace via the ESO delivery bundle, agent status streaming on the new
// GVRs reaches the Resources Inventory, and addon adopt/update rejects
// schema-invalid values with 422 field-level errors. D7 scope: clean
// recreate only — no operator, no adoption, no migration.
//
// Harness adaptations (documented in the validation report):
//   - ArgoCD is NOT part of the golden-path stack (GAP(rbac-e2e-argocd));
//     this suite installs it via scripts/install-argocd.sh.
//   - The local git provider stamps file:// URLs the in-cluster ArgoCD
//     cannot clone; the harness rewrites them to a git:// daemon bound to
//     the same hostPath before sync (URL is environment plumbing, not
//     render content).
//   - The suite host is not the docker host in this environment (nested
//     container), so the hostPath git root is unreachable from the test
//     process — host-side git operations exec into the git-daemon pod
//     (which mounts the same hostPath) instead of -C paths.
//   - The agent chart's discovery ClusterRole (W1-3) covers
//     cert-manager.io/externaldns.k8s.io/argoproj.io but NOT
//     stacks.inari.io/identity.inari.io; the harness grants those reads so
//     the end-to-end mechanics are provable (chart gap = report finding).
//   - The server renders no ClusterIssuer (per-tenant render covers
//     project/dns/namespace/realm); a self-signed ClusterIssuer fixture
//     proves the native-resource path, and the absence of issuer rendering
//     is a report finding.
//   - Keycloak realm assertions use a master-realm bootstrap token
//     (keycloak-initial-admin): the inari-realm platform admin client
//     cannot see tenant realms.
func RenderPath(t *testing.T) {
	e := Load(t)
	// The toolbox is a bare `sleep 3600` pod that expires on long-running
	// validation sessions — every API call execs through it, so keep it
	// alive (idempotent: recreates only when not Running).
	if phase, err := kube.Kubectl("-n", e.Namespace, "get", "pod", e.Toolbox,
		"-o", "jsonpath={.status.phase}"); err != nil || phase != "Running" {
		_, _ = kube.Kubectl("-n", e.Namespace, "delete", "pod", e.Toolbox,
			"--ignore-not-found", "--wait=false")
		if _, err := kube.Kubectl("-n", e.Namespace, "run", e.Toolbox,
			"--image=curlimages/curl:8.10.1", "--restart=Never",
			`--overrides={"spec":{"securityContext":{"runAsUser":1000}}}`,
			"--command", "--", "sleep", "7200"); err != nil {
			t.Fatalf("recreate toolbox: %v", err)
		}
		if err := kube.WaitCondition(e.Namespace, "condition=ready", "120s", "pod/"+e.Toolbox); err != nil {
			t.Fatalf("toolbox not ready: %v", err)
		}
	}
	r := &renderPath{
		Env:       e,
		tenantNS:  "tenant-" + e.Tenant,
		chartsDir: envOr("HELM_CHARTS_DIR", filepath.Clean(filepath.Join(stackDir(), "../../.."))),
		cluster:   envOr("CLUSTER_NAME", "inari-e2e"),
		repoName:  "platform",
		fileURL:   "file:///var/lib/inari/git/platform.git",
		gitURL:    "git://git-daemon.argocd.svc:9418/platform.git",
	}
	t.Run("argocd_install", r.argocdInstall)
	t.Run("git_daemon", r.gitDaemon)
	t.Run("platform_graphs", r.platformGraphs)
	t.Run("render_cutover", r.renderCutover)
	t.Run("render_commit", r.renderCommit)
	t.Run("argocd_sync", r.argocdSync)
	t.Run("appproject_enforced", r.appProjectEnforced)
	t.Run("dnsendpoint_accepted", r.dnsEndpointAccepted)
	t.Run("kro_children", r.kroChildren)
	t.Run("clusterissuer_ready", r.clusterIssuerFixture)
	t.Run("keycloak_realm_ready", r.keycloakRealmReady)
	t.Run("keycloak_client_secret", r.keycloakClientSecret)
	t.Run("keycloak_teardown", r.keycloakTeardown)
	t.Run("status_streaming", r.statusStreaming)
	t.Run("addon_schema_422", r.addonSchema422)
	t.Run("rgd_teardown", r.rgdTeardown)
}

type renderPath struct {
	*Env
	tenantNS  string
	chartsDir string
	cluster   string
	repoName  string // INARI_PLATFORM_GITOPS_REPO value
	fileURL   string // URL the local git provider stamps into renders
	gitURL    string // in-cluster git daemon URL ArgoCD clones
	gitPod    string // resolved git-daemon pod name (cached)
}

// --- setup phases ---------------------------------------------------------

// argocdInstall side-loads the (docker-cached) ArgoCD images and runs the
// idempotent day-0 installer.
func (r *renderPath) argocdInstall(t *testing.T) {
	if err := kind.LoadImages(r.cluster,
		"quay.io/argoproj/argocd:v3.1.5",
		"public.ecr.aws/docker/library/redis:7.2.7-alpine",
		"ghcr.io/dexidp/dex:v2.43.0",
		"golang:1.27",
	); err != nil {
		t.Fatalf("kind load images: %v", err)
	}
	if _, err := kube.Run("bash", filepath.Join(r.chartsDir, "scripts/install-argocd.sh")); err != nil {
		t.Fatalf("install-argocd.sh: %v", err)
	}
}

// gitDaemon serves the kind node's /git hostPath (the local git provider's
// root) over git:// so the in-cluster ArgoCD can clone the platform repo.
func (r *renderPath) gitDaemon(t *testing.T) {
	manifest := `
apiVersion: apps/v1
kind: Deployment
metadata:
  name: git-daemon
  namespace: argocd
spec:
  replicas: 1
  selector:
    matchLabels: {app: git-daemon}
  template:
    metadata:
      labels: {app: git-daemon}
    spec:
      containers:
        - name: git-daemon
          image: golang:1.27
          imagePullPolicy: IfNotPresent
          # safe.directory: repos are created by the server pod (uid 65532);
          # the daemon runs as root and git >= 2.35.2 refuses foreign-owned
          # repos without the exception. The same exception covers the
          # harness's in-pod git operations (kubectl exec).
          command: ["sh", "-c", "git config --global --add safe.directory '*' && exec git daemon --base-path=/git --export-all --reuseaddr --listen=0.0.0.0 --port=9418"]
          ports:
            - containerPort: 9418
          volumeMounts:
            - {name: git, mountPath: /git}
      volumes:
        - name: git
          hostPath: {path: /git, type: Directory}
---
apiVersion: v1
kind: Service
metadata:
  name: git-daemon
  namespace: argocd
spec:
  selector: {app: git-daemon}
  ports:
    - {port: 9418, targetPort: 9418}
`
	if _, err := kube.ApplyStdin(manifest); err != nil {
		t.Fatalf("git daemon manifest: %v", err)
	}
	require.NoError(t, kube.RolloutStatus("argocd", "deployment/git-daemon", "120s"))
}

// gitInPod runs git inside the git-daemon pod (which mounts the same /git
// hostPath the server writes to) — the harness's only channel to the repos,
// since the suite host is not the docker host here.
func (r *renderPath) gitInPod(t *testing.T, args ...string) (string, error) {
	t.Helper()
	if r.gitPod == "" {
		pod, err := kube.Kubectl("-n", "argocd", "get", "pod", "-l", "app=git-daemon",
			"-o", "jsonpath={.items[0].metadata.name}")
		if err != nil {
			return "", err
		}
		r.gitPod = pod
	}
	return kube.ExecInPod("argocd", r.gitPod, args...)
}

// platformGraphs installs the W1-2 chart (tenantnamespace RGD + Keycloak
// XRDs/Compositions) the way gitops/platform/platform-rgd.yaml does,
// directly from the working tree. Phase 3: keycloak.enabled=true — the XRs
// reconcile against the mirrored Crossplane 2.4.3 + provider-keycloak +
// function-go-templating, with credentials from the inari-platform
// provisioner secret.
func (r *renderPath) platformGraphs(t *testing.T) {
	r.ensureProvisionerSecret(t)
	require.NoError(t, helm.UpgradeInstall("platform-graphs",
		filepath.Join(r.chartsDir, "charts/inari-platform-graphs"),
		"--namespace", "default",
		"--set", "tenantNamespace.enabled=true",
		"--set", "keycloak.enabled=true",
		"--set", "keycloak.issuerBaseURL="+r.issuerBaseURL(),
		"--wait", "--timeout", "120s"))
	poll.Eventually(t, 180*time.Second, 5*time.Second, func() (bool, error) {
		state, err := kube.Kubectl("get", "resourcegraphdefinition", "tenantnamespace",
			"-o", "jsonpath={.status.state}")
		return err == nil && state == "Active", nil
	}, "tenantnamespace RGD Active")
	// KRO generates the instance CRD once the RGD is Active; Crossplane
	// serves the XR CRDs once the XRDs are established.
	require.NoError(t, kube.WaitCondition("", "condition=established", "180s",
		"crd/tenantnamespaces.stacks.inari.io"))
	require.NoError(t, kube.WaitCondition("", "condition=established", "180s",
		"crd/tenantrealms.identity.inari.io"))
	require.NoError(t, kube.WaitCondition("", "condition=established", "180s",
		"crd/tenantclients.identity.inari.io"))
}

// issuerBaseURL is the hub Keycloak's issuer base in this stack (the
// golden path pins the issuer hostname to the in-cluster service FQDN).
func (r *renderPath) issuerBaseURL() string {
	return "http://keycloak-service." + r.Namespace + ".svc:8080"
}

// ensureProvisionerSecret guarantees the Phase 3 credentials wiring exists:
// fresh provisions render the provisioner secret + client-setup hook from
// the working tree, but a handoff rerun may attach to a stack provisioned
// before the wiring landed — upgrade platform-config in that case only.
func (r *renderPath) ensureProvisionerSecret(t *testing.T) {
	t.Helper()
	if _, err := kube.Kubectl("-n", r.Namespace, "get", "secret", "inari-keycloak-provisioner"); err == nil {
		return
	}
	t.Log("inari-keycloak-provisioner missing (pre-Phase-3 stack) — upgrading platform-config from the working tree")
	require.NoError(t, helm.UpgradeInstall("platform-config",
		filepath.Join(r.chartsDir, "charts/inari-platform"),
		"--namespace", r.Namespace, "--reuse-values",
		"--wait", "--timeout", "10m"))
}

// renderCutover points the server at the platform repo and flips the
// render-cutover flag, then triggers a reconcile so the tenant's manifests
// are re-rendered as source descriptors and committed.
func (r *renderPath) renderCutover(t *testing.T) {
	if _, err := kube.Kubectl("-n", r.Namespace, "set", "env",
		"deployment/inari-server", "INARI_PLATFORM_GITOPS_REPO="+r.repoName); err != nil {
		t.Fatalf("set INARI_PLATFORM_GITOPS_REPO: %v", err)
	}
	require.NoError(t, kube.RolloutStatus(r.Namespace, "deployment/inari-server", "300s"))

	code, body, err := r.API.SetPlatformFeatureFlag("platform_resources.gitops_render", true)
	require.NoError(t, err)
	require.Contains(t, []int{200, 204}, code, "flag set: %s", body)

	code, body, err = r.API.ReconcilePlatformResources()
	require.NoError(t, err)
	require.Equal(t, 202, code, "reconcile: %s", body)
}

// renderCommit polls the platform repo for the descriptor layout the
// renderer emits (render.go renderTenantDescriptors) — read-only ops run
// against the bare repo inside the git-daemon pod.
func (r *renderPath) renderCommit(t *testing.T) {
	var tree string
	poll.Eventually(t, 120*time.Second, 3*time.Second, func() (bool, error) {
		out, err := r.gitInPod(t, "git", "--git-dir=/git/platform.git",
			"ls-tree", "-r", "--name-only", "main")
		if err != nil {
			return false, nil // repo not created yet (reconcile pending)
		}
		tree = out
		return true, nil
	}, "platform repo created + first descriptor commit (server logs: kubectl -n "+r.Namespace+" logs deploy/inari-server)")

	for _, p := range []string{
		"tenants/" + r.Tenant + "/project/application.yaml",
		"tenants/" + r.Tenant + "/dns/application.yaml",
		"tenants/" + r.Tenant + "/namespace/application.yaml",
		"tenants/" + r.Tenant + "/namespace/instance.yaml",
		"tenants/" + r.Tenant + "/keycloak/realm/application.yaml",
	} {
		assert.Contains(t, tree, p, "descriptor layout missing %s", p)
	}
	// D7 sanity: the legacy platform.inari.io CR manifest paths must NOT be
	// present — the flag was flipped before the first render to this repo.
	for _, legacy := range []string{
		"tenants/" + r.Tenant + "/keycloak-realm.yaml",
		"tenants/" + r.Tenant + "/dns-record.yaml",
		"tenants/" + r.Tenant + "/tenant-namespace.yaml",
	} {
		assert.NotContains(t, tree, legacy, "legacy CR render path leaked into the descriptor commit")
	}
	// The KRO instance targets the RGD's generated GVR (stacks.inari.io).
	inst, err := r.gitInPod(t, "git", "--git-dir=/git/platform.git", "show",
		"main:tenants/"+r.Tenant+"/namespace/instance.yaml")
	require.NoError(t, err)
	assert.Contains(t, inst, "apiVersion: stacks.inari.io/v1alpha1")
	assert.Contains(t, inst, "kind: TenantNamespace")
	assert.Contains(t, inst, "name: "+r.tenantNS)

	// The realm render targets the v2 namespaced-XR coordinates
	// (identity.inari.io/v1alpha1 TenantRealm, tenant namespace).
	realmFiles, err := r.gitInPod(t, "sh", "-c",
		"git --git-dir=/git/platform.git ls-tree -r --name-only main -- "+
			"tenants/"+r.Tenant+"/keycloak/realm/manifests/")
	require.NoError(t, err)
	realmFile := strings.TrimSpace(realmFiles)
	require.NotEmpty(t, realmFile, "realm manifests missing from the descriptor commit")
	realmDoc, err := r.gitInPod(t, "git", "--git-dir=/git/platform.git", "show", "main:"+realmFile)
	require.NoError(t, err)
	assert.Contains(t, realmDoc, "apiVersion: identity.inari.io/v1alpha1")
	assert.Contains(t, realmDoc, "kind: TenantRealm")
	assert.Contains(t, realmDoc, "namespace: "+r.tenantNS)
}

// argocdSync rewrites the file:// repo URLs to the git daemon (documented
// harness adaptation) inside the daemon pod, pushes over the local
// filesystem path, then syncs the tenant tree through a root app-of-apps
// and waits for the child Applications to converge.
func (r *renderPath) argocdSync(t *testing.T) {
	// Clone → rewrite repo URLs → add the ClusterIssuer fixture → push, all
	// in-pod (file paths, no network git). Idempotent: a rerun after a
	// partially successful earlier attempt finds URLs already rewritten and
	// skips the commit instead of failing.
	if _, err := r.gitInPod(t, "sh", "-c",
		"rm -rf /tmp/work && git clone -q /git/platform.git /tmp/work"); err != nil {
		t.Fatalf("in-pod clone: %v", err)
	}
	out, err := r.gitInPod(t, "sh", "-c",
		"cd /tmp/work && grep -rl '"+r.fileURL+"' . --exclude-dir=.git | wc -l")
	require.NoError(t, err)
	if strings.TrimSpace(out) == "0" {
		// Already rewritten by an earlier run — verify rather than fail.
		_, checkErr := r.gitInPod(t, "sh", "-c",
			"cd /tmp/work && grep -rq '"+r.gitURL+"' . --exclude-dir=.git")
		require.NoError(t, checkErr,
			"platform repo carries neither %s nor %s — render shape changed?", r.fileURL, r.gitURL)
		t.Log("repo URLs already rewritten (rerun) — skipping rewrite/push")
	} else {
		if _, err := r.gitInPod(t, "sh", "-c",
			"cd /tmp/work && grep -rl '"+r.fileURL+"' . --exclude-dir=.git | "+
				"xargs sed -i 's|"+r.fileURL+"|"+r.gitURL+"|g'"); err != nil {
			t.Fatalf("rewrite repo URLs: %v", err)
		}
	}

	// Harness-only ClusterIssuer fixture (finding: the server renders no
	// ClusterIssuer; per-tenant render covers project/dns/namespace/realm).
	if _, err := r.gitInPod(t, "sh", "-c",
		"mkdir -p /tmp/work/harness/clusterissuer"); err != nil {
		t.Fatalf("fixture dir: %v", err)
	}
	if _, err := kube.ExecInPodStdin("argocd", r.gitPod, `
apiVersion: cert-manager.io/v1
kind: ClusterIssuer
metadata:
  name: e2e-selfsigned
spec:
  selfSigned: {}
`, "sh", "-c", "cat > /tmp/work/harness/clusterissuer/issuer.yaml"); err != nil {
		t.Fatalf("fixture write: %v", err)
	}

	// Commit + push only when the tree actually changed (skip-if-clean must
	// not mask a genuine commit/push failure, so no blanket `|| true`). The
	// trailing chown restores the server pod's ownership (in-pod ops run as
	// root; without it the server's next reconcile fails updating refs —
	// EACCES).
	if _, err := r.gitInPod(t, "sh", "-c",
		"cd /tmp/work && git add -A && "+
			"if ! git diff --cached --quiet; then "+
			"git -c user.email=e2e@inari.local -c user.name=e2e commit -q "+
			"-m 'test: rewrite repo URLs for in-cluster ArgoCD; add ClusterIssuer fixture' && "+
			"git push -q origin main || exit 1; "+
			"fi; "+
			"chown -R 65532:65532 /git/platform.git"); err != nil {
		t.Fatalf("in-pod commit/push: %v", err)
	}

	// Remove any root app from earlier harness iterations (superseded
	// design) BEFORE applying children so it cannot fight over the same
	// Application objects.
	_, _ = kube.Kubectl("-n", "argocd", "delete", "application", "platform-tenant-root",
		"--wait=false", "--ignore-not-found")

	// Apply the server-rendered child Applications directly (the app-of-apps
	// root is bootstrap plumbing, not render output; a directory-recurse
	// root would double-track the raw manifests). The realm child app is
	// included since Phase 3 — the XR CRDs exist on this stack.
	for _, component := range []string{"project", "dns", "namespace", "keycloak/realm"} {
		appYAML, err := r.gitInPod(t, "cat",
			"/tmp/work/tenants/"+r.Tenant+"/"+component+"/application.yaml")
		require.NoError(t, err)
		if _, err := kube.ApplyStdin(appYAML); err != nil {
			t.Fatalf("apply %s application: %v", component, err)
		}
	}
	harnessApp := `
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: harness-clusterissuer
  namespace: argocd
spec:
  project: default
  source:
    repoURL: ` + r.gitURL + `
    targetRevision: main
    path: harness/clusterissuer
  destination:
    server: https://kubernetes.default.svc
    namespace: default
  syncPolicy:
    automated:
      prune: true
      selfHeal: true
`
	if _, err := kube.ApplyStdin(harnessApp); err != nil {
		t.Fatalf("harness clusterissuer application: %v", err)
	}

	for _, app := range []string{
		"tenant-" + r.Tenant + "-project",
		"tenant-" + r.Tenant + "-dns",
		"tenant-" + r.Tenant + "-namespace",
		"tenant-" + r.Tenant + "-realm",
	} {
		poll.Eventually(t, 300*time.Second, 5*time.Second, func() (bool, error) {
			out, err := kube.Kubectl("-n", "argocd", "get", "application", app,
				"-o", "jsonpath={.status.sync.status}/{.status.health.status}")
			if err != nil {
				return false, nil
			}
			t.Logf("app %s: %s", app, out)
			return out == "Synced/Healthy", nil
		}, "ArgoCD app "+app+" Synced/Healthy")
	}
}

// --- behavior assertions ---------------------------------------------------

// appProjectEnforced checks the rendered AppProject's guardrails and that
// ArgoCD actually enforces them against an out-of-policy Application.
func (r *renderPath) appProjectEnforced(t *testing.T) {
	out, err := kube.Kubectl("-n", "argocd", "get", "appproject", r.tenantNS, "-o", "json")
	require.NoError(t, err, "tenant AppProject missing")
	var proj struct {
		Spec struct {
			SourceRepos  []string `json:"sourceRepos"`
			Destinations []struct {
				Server    string `json:"server"`
				Namespace string `json:"namespace"`
			} `json:"destinations"`
		} `json:"spec"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &proj))
	assert.Contains(t, proj.Spec.SourceRepos, r.gitURL,
		"AppProject must allow the platform repo (rewritten URL)")
	assert.Contains(t, proj.Spec.SourceRepos, "https://charts.dexidp.io",
		"dex chart repo auto-union (ported from argoproject_controller.go)")
	require.Len(t, proj.Spec.Destinations, 1)
	assert.Equal(t, "https://kubernetes.default.svc", proj.Spec.Destinations[0].Server)
	assert.Equal(t, r.tenantNS, proj.Spec.Destinations[0].Namespace,
		"tenant project must pin destinations to the tenant namespace (D3)")

	// Enforcement proof: an Application in the tenant project targeting a
	// foreign namespace must be rejected by project admission.
	rogue := `
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: rogue-` + r.Tenant + `
  namespace: argocd
spec:
  project: ` + r.tenantNS + `
  source:
    repoURL: ` + r.gitURL + `
    targetRevision: main
    path: tenants/` + r.Tenant + `/dns/manifests
  destination:
    server: https://kubernetes.default.svc
    namespace: kube-system
`
	if _, err := kube.ApplyStdin(rogue); err != nil {
		t.Fatalf("rogue application: %v", err)
	}
	defer kube.Kubectl("-n", "argocd", "delete", "application", "rogue-"+r.Tenant, "--wait=false")
	if err := poll.Until(120*time.Second, 5*time.Second, func() (bool, error) {
		out, err := kube.Kubectl("-n", "argocd", "get", "application", "rogue-"+r.Tenant,
			"-o", "jsonpath={.status}")
		if err != nil {
			return false, nil
		}
		return strings.Contains(out, "not permitted"), nil
	}); err != nil {
		dump, _ := kube.Kubectl("-n", "argocd", "get", "application", "rogue-"+r.Tenant,
			"-o", "jsonpath={.status}")
		t.Fatalf("rogue app never rejected by AppProject destination policy; status: %.1200s", dump)
	}
}

// dnsEndpointAccepted proves ExternalDNS picked the rendered DNSEndpoint up
// (the e2e provider is a dead rfc2136 address by design, so evidence is the
// record entering the sync loop's plan, not a real DNS mutation).
func (r *renderPath) dnsEndpointAccepted(t *testing.T) {
	if _, err := kube.Kubectl("-n", r.tenantNS, "get", "dnsendpoint", r.Tenant); err != nil {
		t.Fatalf("DNSEndpoint %s not synced into %s: %v", r.Tenant, r.tenantNS, err)
	}
	// Surface info-level plan logs and force an immediate sync loop.
	require.NoError(t, helm.UpgradeInstall("external-dns", "external-dns/external-dns",
		"--namespace", "external-dns", "--reuse-values", "--set", "logLevel=info"))
	require.NoError(t, kube.RolloutStatus("external-dns", "deployment/external-dns", "120s"))
	poll.Eventually(t, 180*time.Second, 10*time.Second, func() (bool, error) {
		logs, err := kube.Logs("external-dns", "app.kubernetes.io/name=external-dns")
		return err == nil && strings.Contains(logs, r.tenantNS+".7kgroup.org"), nil
	}, "external-dns sync loop accepted "+r.tenantNS+".7kgroup.org")
}

// kroChildren asserts the RGD instance materialized exactly the operator's
// old TenantNamespace child set (minus impersonation, per D3).
func (r *renderPath) kroChildren(t *testing.T) {
	require.NoError(t, kube.WaitCondition("default", "condition=Ready", "180s",
		"tenantnamespace.stacks.inari.io/"+r.tenantNS))
	for _, res := range []string{"namespace/" + r.tenantNS} {
		if _, err := kube.Kubectl("get", res); err != nil {
			t.Errorf("missing child %s: %v", res, err)
		}
	}
	for _, res := range []string{"rolebinding/tenant-admin", "networkpolicy/tenant-default-deny"} {
		if _, err := kube.Kubectl("-n", r.tenantNS, "get", res); err != nil {
			t.Errorf("missing child %s in %s: %v", res, r.tenantNS, err)
		}
	}
	// No quota requested in the instance spec → no ResourceQuota child.
	_, err := kube.Kubectl("-n", r.tenantNS, "get", "resourcequota/tenant-quota")
	assert.Error(t, err, "ResourceQuota must be absent when spec.resourceQuota is unset")
	// RoleBinding shape: ClusterRole admin, no subjects (no adminGroup).
	rb, err := kube.Kubectl("-n", r.tenantNS, "get", "rolebinding/tenant-admin",
		"-o", "jsonpath={.roleRef.name}")
	require.NoError(t, err)
	assert.Equal(t, "admin", rb)
}

// clusterIssuerFixture waits for the harness ClusterIssuer to go Ready —
// proving the native cert-manager resource path on this cluster (the
// server-side render of issuers is a documented finding, not covered here).
func (r *renderPath) clusterIssuerFixture(t *testing.T) {
	require.NoError(t, kube.WaitCondition("", "condition=Ready", "120s",
		"clusterissuer/e2e-selfsigned"))
}

// keycloakRealmReady proves the server-rendered TenantRealm XR reconciles
// through provider-keycloak (Phase 3 exit criterion): Ready condition, the
// projected status contract (status.realm / status.issuerURL), and the
// realm actually existing in Keycloak. Realm assertions need a master-realm
// token — the inari-realm admin client cannot see other realms.
func (r *renderPath) keycloakRealmReady(t *testing.T) {
	require.NoError(t, kube.WaitCondition(r.tenantNS, "condition=Ready", "420s",
		"tenantrealm.identity.inari.io/"+r.Tenant))
	poll.Eventually(t, 180*time.Second, 5*time.Second, func() (bool, error) {
		token, err := r.KC.MasterToken() // short-lived; refresh per attempt
		if err != nil {
			return false, nil
		}
		return r.KC.RealmExists(token, r.Tenant), nil
	}, "tenant realm "+r.Tenant+" exists in Keycloak")
	status, err := kube.Kubectl("-n", r.tenantNS, "get", "tenantrealm.identity.inari.io", r.Tenant,
		"-o", "jsonpath={.status.realm}/{.status.issuerURL}")
	require.NoError(t, err)
	assert.Equal(t, r.Tenant+"/"+r.issuerBaseURL()+"/realms/"+r.Tenant, status,
		"composition must project the operator's status contract (realm + issuerURL)")
}

// keycloakClientSecret proves the client credentials contract (Phase 3 exit
// criterion): a confidential TenantClient XR → provider-keycloak writes the
// connection secret into the tenant namespace → the ESO SecretStore +
// ExternalSecret pair materializes the operator-compatible Secret (keys
// client-id / client-secret / issuer) — and the secret payload matches the
// client provider-keycloak actually created in Keycloak.
func (r *renderPath) keycloakClientSecret(t *testing.T) {
	const client = "e2e-workload-client"
	issuer := r.issuerBaseURL() + "/realms/" + r.Tenant
	// Mirrors the server-rendered bundle
	// (charts/inari-platform-graphs/examples/keycloak-client-secret-delivery.yaml)
	// applied directly: no registration-time client claim exists on this
	// stack (the render flag was off when the seed cluster registered).
	fixture := `
apiVersion: v1
kind: ServiceAccount
metadata:
  name: eso-tenant-secret-reader
  namespace: ` + r.tenantNS + `
---
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: eso-tenant-secret-reader
  namespace: ` + r.tenantNS + `
rules:
  - apiGroups: [""]
    resources: ["secrets"]
    verbs: ["get", "list", "watch"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: eso-tenant-secret-reader
  namespace: ` + r.tenantNS + `
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: Role
  name: eso-tenant-secret-reader
subjects:
  - kind: ServiceAccount
    name: eso-tenant-secret-reader
    namespace: ` + r.tenantNS + `
---
apiVersion: external-secrets.io/v1
kind: SecretStore
metadata:
  name: keycloak-client-connections
  namespace: ` + r.tenantNS + `
spec:
  provider:
    kubernetes:
      server:
        url: https://kubernetes.default.svc
        caProvider:
          type: ConfigMap
          name: kube-root-ca.crt
          key: ca.crt
          namespace: ` + r.tenantNS + `
      auth:
        serviceAccount:
          name: eso-tenant-secret-reader
---
apiVersion: identity.inari.io/v1alpha1
kind: TenantClient
metadata:
  name: ` + client + `
  namespace: ` + r.tenantNS + `
spec:
  tenantID: ` + r.Tenant + `
  namespace: ` + r.tenantNS + `
  realm: ` + r.Tenant + `
  clientID: ` + client + `
  public: false
  serviceAccountsEnabled: true
---
apiVersion: external-secrets.io/v1
kind: ExternalSecret
metadata:
  name: ` + client + `-keycloak-client
  namespace: ` + r.tenantNS + `
spec:
  refreshInterval: 1m
  secretStoreRef:
    kind: SecretStore
    name: keycloak-client-connections
  target:
    name: ` + client + `-keycloak-client
    template:
      type: Opaque
      metadata:
        labels:
          platform.inari.io/tenant: ` + r.Tenant + `
      data:
        client-id: "` + client + `"
        issuer: "` + issuer + `"
        client-secret: "{{ .client_secret }}"
  data:
    - secretKey: client_secret
      remoteRef:
        key: ` + client + `-keycloak-connection
        property: attribute.client_secret
`
	if _, err := kube.ApplyStdin(fixture); err != nil {
		t.Fatalf("client fixture: %v", err)
	}

	// The provider must create the client in the tenant realm.
	require.NoError(t, kube.WaitCondition(r.tenantNS, "condition=Ready", "420s",
		"tenantclient.identity.inari.io/"+client))
	poll.Eventually(t, 180*time.Second, 5*time.Second, func() (bool, error) {
		token, err := r.KC.MasterToken()
		if err != nil {
			return false, nil
		}
		id, err := r.KC.FindClientIn(token, r.Tenant, client)
		return err == nil && id != "", nil
	}, "client "+client+" exists in tenant realm "+r.Tenant)

	// The provider wrote the connection secret into the tenant namespace…
	var connSecret string
	poll.Eventually(t, 180*time.Second, 5*time.Second, func() (bool, error) {
		b64, err := kube.Kubectl("-n", r.tenantNS, "get", "secret", client+"-keycloak-connection",
			"-o", `jsonpath={.data.attribute\.client_secret}`)
		if err != nil || b64 == "" {
			return false, nil
		}
		raw, err := base64.StdEncoding.DecodeString(b64)
		if err != nil {
			return false, nil
		}
		connSecret = string(raw)
		return true, nil
	}, "provider connection secret "+client+"-keycloak-connection written")

	// …and ESO materialized the operator-compatible credentials Secret from
	// it (same value, contract keys).
	require.NoError(t, kube.WaitCondition(r.tenantNS, "condition=Ready", "180s",
		"externalsecret/"+client+"-keycloak-client"))
	secretField := func(key string) string {
		b64, err := kube.Kubectl("-n", r.tenantNS, "get", "secret", client+"-keycloak-client",
			"-o", "jsonpath={.data."+key+"}")
		require.NoError(t, err)
		raw, err := base64.StdEncoding.DecodeString(b64)
		require.NoError(t, err)
		return string(raw)
	}
	assert.Equal(t, client, secretField("client-id"))
	assert.Equal(t, issuer, secretField("issuer"))
	assert.NotEmpty(t, connSecret, "connection secret attribute.client_secret empty")
	assert.Equal(t, connSecret, secretField("client-secret"),
		"delivered client-secret must equal the provider-written connection secret value")

	// Projected XR status contract.
	status, err := kube.Kubectl("-n", r.tenantNS, "get", "tenantclient.identity.inari.io", client,
		"-o", "jsonpath={.status.clientID}/{.status.realm}/{.status.secretName}")
	require.NoError(t, err)
	assert.Equal(t, client+"/"+r.Tenant+"/"+client+"-keycloak-client", status)
}

// keycloakTeardown verifies the provider-keycloak deletion path on a
// DISPOSABLE realm (Phase 3 exit criterion; the tenant's own realm is never
// deleted here): deleting the TenantRealm XR cascades XR → Realm MR →
// Keycloak realm, with no finalizer residue.
func (r *renderPath) keycloakTeardown(t *testing.T) {
	const name = "e2e-disposable"
	fixture := `
apiVersion: identity.inari.io/v1alpha1
kind: TenantRealm
metadata:
  name: ` + name + `
  namespace: ` + r.tenantNS + `
spec:
  tenantID: ` + r.Tenant + `
  namespace: ` + r.tenantNS + `
  realm: ` + name + `
  enabled: true
`
	if _, err := kube.ApplyStdin(fixture); err != nil {
		t.Fatalf("disposable realm fixture: %v", err)
	}
	require.NoError(t, kube.WaitCondition(r.tenantNS, "condition=Ready", "420s",
		"tenantrealm.identity.inari.io/"+name))
	poll.Eventually(t, 120*time.Second, 5*time.Second, func() (bool, error) {
		token, err := r.KC.MasterToken()
		if err != nil {
			return false, nil
		}
		return r.KC.RealmExists(token, name), nil
	}, "disposable realm exists in Keycloak")

	if _, err := kube.Kubectl("-n", r.tenantNS, "delete", "tenantrealm.identity.inari.io", name,
		"--wait=false"); err != nil {
		t.Fatalf("delete disposable realm XR: %v", err)
	}
	poll.Eventually(t, 180*time.Second, 5*time.Second, func() (bool, error) {
		_, err := kube.Kubectl("-n", r.tenantNS, "get", "tenantrealm.identity.inari.io", name)
		return err != nil, nil // XR gone (no stuck finalizer)
	}, "disposable TenantRealm XR deleted")
	poll.Eventually(t, 180*time.Second, 5*time.Second, func() (bool, error) {
		_, err := kube.Kubectl("-n", r.tenantNS, "get", "realm.realm.keycloak.m.crossplane.io", name)
		return err != nil, nil // composed Realm MR gone
	}, "composed Realm managed resource deleted")
	poll.Eventually(t, 180*time.Second, 5*time.Second, func() (bool, error) {
		token, err := r.KC.MasterToken()
		if err != nil {
			return false, nil
		}
		return !r.KC.RealmExists(token, name), nil
	}, "disposable realm deleted from Keycloak")
}

// statusStreaming configures the agent's watch list for the new GVRs
// (W1-3's INARI_STATUS_WATCHES), grants the reads the chart RBAC lacks
// (finding), and asserts the rendered resources' status reaches the
// Resources Inventory rows.
func (r *renderPath) statusStreaming(t *testing.T) {
	// Chart-RBAC gap workaround (report finding: clusterrole.yaml grants
	// cert-manager.io/externaldns.k8s.io/argoproj.io but not the RGD/claim
	// groups).
	grant := `
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: inari-agent-status-native-e2e
rules:
  - apiGroups: ["stacks.inari.io"]
    resources: ["tenantnamespaces"]
    verbs: ["get", "list", "watch"]
  - apiGroups: ["identity.inari.io"]
    resources: ["tenantrealms", "tenantclients"]
    verbs: ["get", "list", "watch"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: inari-agent-status-native-e2e
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: inari-agent-status-native-e2e
subjects:
  - kind: ServiceAccount
    name: inari-agent
    namespace: inari-system
`
	if _, err := kube.ApplyStdin(grant); err != nil {
		t.Fatalf("agent status RBAC grant: %v", err)
	}

	require.NoError(t, helm.UpgradeInstall("inari-agent", r.AgentChartDir,
		"--namespace", "default", "--reuse-values",
		"--set", "config.statusWatches="+renderStatusWatches()))
	require.NoError(t, kube.RolloutStatus("inari-system", "deployment/inari-agent", "180s"))

	// Agent reconnects and streams the current state of the watched GVRs.
	poll.Eventually(t, 120*time.Second, 5*time.Second, func() (bool, error) {
		cl, err := r.API.GetCluster(r.ClusterID)
		return err == nil && cl.State == "active", nil
	}, "agent reconnected with the new watch list")

	// The KRO instance (Ready) must land on the tenant-namespace row; the
	// condition-less DNSEndpoint lands on the dns-zone row with a
	// non-ready status (healthOf has no Ready condition to read — the
	// semantic is recorded in the report; the row receiving updates is the
	// streaming proof).
	poll.Eventually(t, 180*time.Second, 5*time.Second, func() (bool, error) {
		rows, err := r.API.ListPlatformResources()
		if err != nil {
			return false, nil
		}
		for _, row := range rows {
			if row.Kind == "tenant-namespace" && row.Name == r.tenantNS {
				t.Logf("tenant-namespace row: status=%s detail=%s", row.Status, row.Detail)
				return row.Status == "ready", nil
			}
		}
		return false, nil
	}, "tenant-namespace row ready via agent status stream")

	rows, err := r.API.ListPlatformResources()
	require.NoError(t, err)
	var dnsSeen bool
	for _, row := range rows {
		if row.Kind == "dns-zone" && row.Name == r.Tenant {
			dnsSeen = true
			t.Logf("dns-zone row: status=%s detail=%s", row.Status, row.Detail)
		}
	}
	assert.True(t, dnsSeen, "dns-zone row present in Resources Inventory")

	// The TenantRealm XR's Ready condition folds onto the keycloak-realm row
	// (renderedKindMap) — the contract the operator's KeycloakRealm CR used
	// to report against.
	poll.Eventually(t, 180*time.Second, 5*time.Second, func() (bool, error) {
		rows, err := r.API.ListPlatformResources()
		if err != nil {
			return false, nil
		}
		for _, row := range rows {
			if row.Kind == "keycloak-realm" && row.Name == r.Tenant {
				t.Logf("keycloak-realm row: status=%s detail=%s", row.Status, row.Detail)
				return row.Status == "ready", nil
			}
		}
		return false, nil
	}, "keycloak-realm row ready via agent status stream")
}

// addonSchema422 is the addon track's exit criterion: schema-invalid values
// are rejected with 422 field-level errors before anything is committed;
// a valid adopt succeeds as the control (and is orphaned afterwards).
func (r *renderPath) addonSchema422(t *testing.T) {
	code, body, err := r.API.AdoptAddon(r.ClusterID, "platform:cert-manager",
		map[string]any{"version": "v1.16.2", "values": map[string]any{"replicaCount": 0}})
	require.NoError(t, err)
	assert.Equal(t, 422, code, "schema-invalid adopt must be rejected: %s", body)
	assert.Contains(t, body, "/replicaCount", "422 must carry the field-level path")

	code, body, err = r.API.AdoptAddon(r.ClusterID, "platform:external-dns",
		map[string]any{"version": "1.15.2", "values": map[string]any{"policy": "upsert-only"}})
	require.NoError(t, err)
	if code == 409 && strings.Contains(body, "no git repo configured") {
		// The golden path never registers a baseline repo for the tenant
		// (Feature A registry requires a GitHub App — N/A with the local
		// provider), so the adopt control cannot commit. The criterion under
		// test (422 schema rejection above) is unaffected; the adopt/update
		// CD loop control is covered by the server's
		// TestAddonsHTTPSupportedAddonsCDLoop integration test (run as part
		// of this validation, see the report).
		t.Logf("control adopt skipped: %s", body)
		return
	}
	require.Contains(t, []int{200, 201}, code, "valid adopt (control): %s", body)
	var out struct {
		Addon struct {
			ID    string `json:"id"`
			State string `json:"state"`
		} `json:"addon"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &out))
	assert.Equal(t, "managed", out.Addon.State)
	code, body, err = r.API.DeleteAddon(out.Addon.ID)
	require.NoError(t, err)
	assert.Contains(t, []int{200, 204}, code, "cleanup orphan: %s", body)
}

// rgdTeardown deletes the KRO instance with ArgoCD self-heal suspended and
// asserts the children cascade via KRO owner refs (no finalizer machinery);
// the agent's delete event must flip the inventory row off ready; then
// ArgoCD re-creates the instance and the row recovers (full round-trip).
func (r *renderPath) rgdTeardown(t *testing.T) {
	nsApp := "tenant-" + r.Tenant + "-namespace"
	// Suspend self-heal (merge-patch null removes the key — an empty-object
	// merge would leave it intact; idempotent across reruns).
	if _, err := kube.Kubectl("-n", "argocd", "patch", "application", nsApp,
		"--type=merge", `-p={"spec":{"syncPolicy":{"automated":null}}}`); err != nil {
		t.Fatalf("suspend self-heal on %s: %v", nsApp, err)
	}
	if _, err := kube.Kubectl("-n", "default", "delete",
		"tenantnamespace.stacks.inari.io", r.tenantNS, "--wait=false", "--ignore-not-found"); err != nil {
		t.Fatalf("delete KRO instance: %v", err)
	}

	// The cascade now runs deeper than the KRO children: the tenant's
	// TenantRealm XR lives in the namespace, so namespace termination waits
	// on the XR finalizer → Realm MR finalizer → provider-keycloak deleting
	// the realm in Keycloak. Generous bound.
	poll.Eventually(t, 420*time.Second, 3*time.Second, func() (bool, error) {
		_, err := kube.Kubectl("get", "namespace", r.tenantNS)
		return err != nil, nil // gone
	}, "namespace child cascaded on instance delete (KRO owner refs)")
	for _, res := range []string{"rolebinding/tenant-admin", "networkpolicy/tenant-default-deny"} {
		_, err := kube.Kubectl("-n", r.tenantNS, "get", res)
		assert.Error(t, err, "orphaned child %s survived instance teardown", res)
	}

	// The agent's delete event (HEALTH_STATUS_MISSING → "reconciling")
	// must flip the inventory row off ready.
	poll.Eventually(t, 120*time.Second, 3*time.Second, func() (bool, error) {
		rows, err := r.API.ListPlatformResources()
		if err != nil {
			return false, nil
		}
		for _, row := range rows {
			if row.Kind == "tenant-namespace" && row.Name == r.tenantNS {
				return row.Status != "ready", nil
			}
		}
		return false, nil
	}, "inventory row left ready after instance delete")

	// Restore automated sync: ArgoCD re-creates the instance, KRO rebuilds
	// the children, and the row returns to ready — recreate path (D7).
	if _, err := kube.Kubectl("-n", "argocd", "patch", "application", nsApp,
		"--type=merge", `-p={"spec":{"syncPolicy":{"automated":{"prune":false,"selfHeal":true}}}}`); err != nil {
		t.Fatalf("restore automated sync on %s: %v", nsApp, err)
	}
	poll.Eventually(t, 300*time.Second, 5*time.Second, func() (bool, error) {
		out, err := kube.Kubectl("-n", "argocd", "get", "application", nsApp,
			"-o", "jsonpath={.status.sync.status}/{.status.health.status}")
		return err == nil && out == "Synced/Healthy", nil
	}, "namespace app re-converged after teardown")
	poll.Eventually(t, 180*time.Second, 5*time.Second, func() (bool, error) {
		rows, err := r.API.ListPlatformResources()
		if err != nil {
			return false, nil
		}
		for _, row := range rows {
			if row.Kind == "tenant-namespace" && row.Name == r.tenantNS {
				return row.Status == "ready", nil
			}
		}
		return false, nil
	}, "inventory row returned to ready after recreate")

	// The tenant realm rides the same recreate path (D7): the namespace
	// cascade deleted the XR (provider-keycloak deleted the Keycloak realm);
	// ArgoCD self-heal re-applied the XR and the provider recreates the
	// realm — declarative recreate, no adoption.
	require.NoError(t, kube.WaitCondition(r.tenantNS, "condition=Ready", "420s",
		"tenantrealm.identity.inari.io/"+r.Tenant))
	poll.Eventually(t, 180*time.Second, 5*time.Second, func() (bool, error) {
		token, err := r.KC.MasterToken()
		if err != nil {
			return false, nil
		}
		return r.KC.RealmExists(token, r.Tenant), nil
	}, "tenant realm recreated in Keycloak after namespace cascade")
}

// --- pure helpers ----------------------------------------------------------

// renderStatusWatches is the INARI_STATUS_WATCHES value for the cutover
// state: the legacy platform.inari.io CRDs (absent here, harmless) plus the
// native replacements and the RGD/claim GVRs (which have no defaults — see
// DefaultWatchResources' comment).
func renderStatusWatches() string {
	return strings.Join([]string{
		"platform.inari.io/v1alpha1/keycloakrealms",
		"platform.inari.io/v1alpha1/keycloakclients",
		"platform.inari.io/v1alpha1/dnszones",
		"platform.inari.io/v1alpha1/dnsrecords",
		"platform.inari.io/v1alpha1/tenantnamespaces",
		"cert-manager.io/v1/clusterissuers",
		"externaldns.k8s.io/v1alpha1/dnsendpoints",
		"argoproj.io/v1alpha1/appprojects",
		"argoproj.io/v1alpha1/applicationsets",
		"stacks.inari.io/v1alpha1/tenantnamespaces",
		"identity.inari.io/v1alpha1/tenantrealms",
		"identity.inari.io/v1alpha1/tenantclients",
	}, ";")
}
