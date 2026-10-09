//go:build e2e

// Golden-path chart provisioning in Go — phases 2–3 of the shell→Go
// migration (parent design §3): every helm install/upgrade sequence from
// the deleted golden-path.sh lives here; kind lifecycle, the git-root
// hostPath, and the KC realm/user seeding joined in phase 3 (provide.go,
// seed.go).
//
// Install dependency graph (edges = hard readiness requirements):
//
//	kind + operators (CNPG, Keycloak operator)     [provide.go]
//	  └─ platform-config (CNPG cluster + db secrets + Keycloak realm)
//	       ├─ Keycloak StatefulSet rollout → hostname patch → rollout
//	       │    (issuer consistency — MUST converge before inari-server)
//	       └─ OpenFGA (postgres datastore via the inari-db secret;
//	          single-replica first — per-pod initContainer goose migrations
//	          have no cross-pod locking; HA scales out after they apply)
//	NATS (JetStream)            — independent of Keycloak/CNPG
//	Vault (dev) + ESO + CRDs    — independent of Keycloak/CNPG
//	BARRIER: inari-server needs Keycloak issuer + NATS + OpenFGA + Vault/ESO
//	inari-operator-crds → inari-operator ┐ independent of each other and
//	inari-console                        ┘ smoke-level alongside the server
//	inari-agent — needs tenant + cluster registration (TestProvisionAgent)
//
// Independent installs run concurrently (errgroup); every async wait is a
// bounded poll via internal/poll or a kubectl/helm readiness gate — no
// fixed sleeps. Stack trimming vs production defaults lives in testdata/
// (single-node NATS + R=1 streams on the non-HA leg, 1Gi PVCs, right-sized
// Keycloak) — see the values files for the per-leg rationale.
package suite

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"golang.org/x/sync/errgroup"

	"7k-inari/inari-release-bundle/scripts/e2e/stack/internal/helm"
	"7k-inari/inari-release-bundle/scripts/e2e/stack/internal/kc"
	"7k-inari/inari-release-bundle/scripts/e2e/stack/internal/kube"
	"7k-inari/inari-release-bundle/scripts/e2e/stack/internal/poll"
)

// ProvisionConfig carries the env knobs the script used to own. Names match
// golden-path.sh exactly so the workflow env passes through unchanged.
type ProvisionConfig struct {
	Namespace         string
	Tenant            string
	Toolbox           string
	ClusterName       string
	KeepCluster       bool
	HA                bool
	CacheBackend      string // memory|redis (default memory; redis when HA)
	ServerImage       string
	AgentImage        string
	VaultDevToken     string
	KindNodeImage     string // KIND_NODE_IMAGE (default: pinned kindest/node)
	AdoptCluster      bool   // E2E_ADOPT_CLUSTER=1 — reuse a pre-created cluster
	GitHostDir        string // GIT_HOST_DIR — fixed git root (CI pre-created cluster)
	RepoRoot          string // HELM_CHARTS_DIR
	PlatformChartDir  string
	ServerChartDir    string
	ConsoleChartDir   string
	AgentChartDir     string
	OperatorChartDir  string
	OperatorCRDsChart string
	ServerReplicas    int
	OpenFGAReplicas   int
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// stackDir resolves the stack module root from this source file, so tests
// find testdata/ regardless of the working directory go test runs in.
func stackDir() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Dir(filepath.Dir(file)) // suite/ -> stack/
}

func testdataFile(name string) string {
	return filepath.Join(stackDir(), "testdata", name)
}

// LoadProvisionConfig reads the provisioning env (same defaults as
// golden-path.sh). HA comes from E2E_HA — the suite's phase-1 knob — which
// replaces the script's remaining INARI_HA helm branches.
func LoadProvisionConfig(t *testing.T) *ProvisionConfig {
	t.Helper()
	repoRoot := envOr("HELM_CHARTS_DIR", filepath.Clean(filepath.Join(stackDir(), "../../..")))
	cfg := &ProvisionConfig{
		Namespace:        envOr("NAMESPACE", "inari"),
		Tenant:           envOr("TENANT", "e2e-org"),
		Toolbox:          envOr("TOOLBOX_POD", "golden-path-tools"),
		ClusterName:      envOr("CLUSTER_NAME", "inari-e2e"),
		KeepCluster:      os.Getenv("KEEP_CLUSTER") == "true",
		HA:               os.Getenv("E2E_HA") == "1",
		ServerImage:      envOr("SERVER_IMAGE", "inari/server:e2e"),
		AgentImage:       envOr("AGENT_IMAGE", "inari/agent:e2e"),
		VaultDevToken:    envOr("VAULT_DEV_TOKEN", "e2e-root-token"),
		KindNodeImage:    envOr("KIND_NODE_IMAGE", defaultKindNodeImage),
		AdoptCluster:     os.Getenv("E2E_ADOPT_CLUSTER") == "1",
		GitHostDir:       os.Getenv("GIT_HOST_DIR"),
		RepoRoot:         repoRoot,
		PlatformChartDir: envOr("PLATFORM_CHART_DIR", filepath.Join(repoRoot, "charts/inari-platform")),
		ServerChartDir:   envOr("SERVER_CHART_DIR", filepath.Join(repoRoot, "charts/inari-server")),
		ConsoleChartDir:  envOr("CONSOLE_CHART_DIR", filepath.Join(repoRoot, "charts/inari-console")),
		AgentChartDir:    envOr("AGENT_CHART_DIR", filepath.Join(repoRoot, "../inari-agent/charts/inari-agent")),
		OperatorChartDir: envOr("OPERATOR_CHART_DIR", filepath.Join(repoRoot, "../inari-operator/charts/inari-operator")),
		ServerReplicas:   1,
		OpenFGAReplicas:  1,
	}
	cfg.OperatorCRDsChart = envOr("OPERATOR_CRDS_CHART_DIR", filepath.Join(cfg.OperatorChartDir, "../inari-operator-crds"))
	if cfg.HA {
		cfg.ServerReplicas = 2
		cfg.OpenFGAReplicas = 2
	}
	// HA defaults to the shared redis cache backend per the chart's
	// multi-replica guidance; non-HA stays per-pod memory (unchanged).
	cfg.CacheBackend = os.Getenv("INARI_E2E_CACHE_BACKEND")
	if cfg.CacheBackend == "" {
		if cfg.HA {
			cfg.CacheBackend = "redis"
		} else {
			cfg.CacheBackend = "memory"
		}
	}
	if cfg.CacheBackend != "memory" && cfg.CacheBackend != "redis" {
		t.Fatalf("INARI_E2E_CACHE_BACKEND must be memory or redis (got: %s)", cfg.CacheBackend)
	}
	for name, dir := range map[string]string{
		"CONSOLE_CHART_DIR": cfg.ConsoleChartDir, "OPERATOR_CHART_DIR": cfg.OperatorChartDir,
		"OPERATOR_CRDS_CHART_DIR": cfg.OperatorCRDsChart, "PLATFORM_CHART_DIR": cfg.PlatformChartDir,
		"SERVER_CHART_DIR": cfg.ServerChartDir,
	} {
		if st, err := os.Stat(dir); err != nil || !st.IsDir() {
			t.Fatalf("%s not found: %s (set %s)", name, dir, name)
		}
	}
	return cfg
}

func (c *ProvisionConfig) kcFQDN() string {
	return "keycloak-service." + c.Namespace + ".svc:8080"
}

// splitImage splits repo:tag, tolerating a registry port (colon before the
// last slash is not a tag) and defaulting a tag-less image to "latest"
// (docker semantics — a bare LastIndex(":") would mis-split or panic).
func splitImage(image string) (repo, tag string) {
	if i := strings.LastIndex(image, ":"); i > strings.LastIndex(image, "/") {
		return image[:i], image[i+1:]
	}
	return image, "latest"
}

func logf(format string, args ...any) { fmt.Printf("[e2e] "+format+"\n", args...) }

// waitCNPG blocks until the CNPG cluster is Ready. Keycloak and OpenFGA
// derive from the platform database; safe to call from multiple goroutines
// (kubectl wait is idempotent).
func waitCNPG(ns string) error {
	return kube.WaitCondition(ns, "condition=Ready", "600s",
		"cluster.postgresql.cnpg.io/postgresql")
}

// helmRepoSetup registers every helm repo SERIALLY (shared local repo cache
// races under concurrency — the reason the script did repo setup up front).
// HELM_REPOS_SEEDED=1 skips add/update entirely: CI restores ~/.config/helm
// + ~/.cache/helm from the helm cache, and add/update would re-fetch every
// index over the network anyway, making the cache dead weight. With the
// restored config+index, helm resolves pinned chart versions offline (the
// small chart tgz still downloads). A stale index missing a newly pinned
// version fails loudly — the correct signal to regenerate images.txt (the
// cache key input).
func helmRepoSetup() error {
	if os.Getenv("HELM_REPOS_SEEDED") == "1" {
		logf("HELM_REPOS_SEEDED=1 — helm repos restored from cache, skipping repo add/update")
		return nil
	}
	for _, r := range [][2]string{
		{"openfga", "https://openfga.github.io/helm-charts"},
		{"nats", "https://nats-io.github.io/k8s/helm/charts/"},
		{"hashicorp", "https://helm.releases.hashicorp.com"},
		{"external-secrets", "https://charts.external-secrets.io"},
		// bitnami is vendored for the inari-server chart's optional redis
		// subchart (helm verifies Chart.yaml dependencies even when disabled).
		{"bitnami", "https://charts.bitnami.com/bitnami"},
	} {
		if err := helm.RepoAdd(r[0], r[1]); err != nil {
			return fmt.Errorf("helm repo add %s: %w", r[0], err)
		}
	}
	return helm.RepoUpdate()
}

// installPlatformConfig: CNPG cluster + db secrets + Keycloak realm import.
func installPlatformConfig(c *ProvisionConfig) error {
	return helm.UpgradeInstall("platform-config", c.PlatformChartDir,
		"--namespace", c.Namespace, "--create-namespace",
		"-f", testdataFile("platform-values-e2e.yaml"),
		"--wait", "--timeout", "10m")
}

// installNATS installs JetStream and (HA leg only) verifies the meta group
// formed with 3 members and a leader — belt-and-braces on top of helm
// --wait; meta-group formation has no API-visible condition, so the bounded
// poll IS the wait. The non-HA leg is a trimmed single node (R=1 streams);
// there is no meta group to verify.
func installNATS(c *ProvisionConfig) error {
	values := "nats-values-e2e.yaml"
	if c.HA {
		values = "nats-values.yaml"
	}
	if err := helm.UpgradeInstall("nats", "nats/nats", "--version", "1.3.2",
		"--namespace", c.Namespace,
		"-f", testdataFile(values),
		// e2e-only: preloaded CI images must be used as-is (the chart's
		// per-image pullPolicy defaults are empty → k8s default, pinned tags
		// already imply IfNotPresent; pin it explicitly for the cache).
		"--set", "global.image.pullPolicy=IfNotPresent",
		"--wait", "--timeout", "8m"); err != nil {
		return err
	}
	if !c.HA {
		return nil
	}
	return poll.Until(120*time.Second, 5*time.Second, func() (bool, error) {
		jsz, err := kube.ExecInPod(c.Namespace, "deploy/nats-box",
			"sh", "-c", "curl -sf http://nats-headless:8222/jsz")
		if err != nil {
			return false, nil // transient: keep polling
		}
		var doc struct {
			MetaCluster struct {
				Size   int    `json:"cluster_size"`
				Leader string `json:"leader"`
			} `json:"meta_cluster"`
		}
		if json.Unmarshal([]byte(jsz), &doc) != nil {
			return false, nil
		}
		return doc.MetaCluster.Size == 3 && doc.MetaCluster.Leader != "", nil
	})
}

// installOpenFGA installs single-replica first (per-pod initContainer goose
// migrations race on a fresh 2-replica install), then HA scales out after
// the migrations have been applied.
func installOpenFGA(c *ProvisionConfig) error {
	if err := waitCNPG(c.Namespace); err != nil {
		return err
	}
	if err := helm.UpgradeInstall("openfga", "openfga/openfga", "--version", "0.2.27",
		"--namespace", c.Namespace,
		"--set", "fullnameOverride=openfga",
		"--set", "replicaCount=1",
		"--set", "datastore.engine=postgres",
		"--set", "datastore.existingSecret=inari-db",
		"--set", "datastore.secretKeys.uriKey=openfga-uri",
		"--set", "datastore.migrationType=initContainer",
		"--set", "playground.enabled=false",
		// e2e-only: the chart defaults image.pullPolicy=Always, which would
		// re-pull every run and defeat the CI image cache.
		"--set", "image.pullPolicy=IfNotPresent",
		"--wait", "--timeout", "5m"); err != nil {
		return err
	}
	if c.OpenFGAReplicas > 1 {
		logf("openfga: HA scaling to %d replicas (migrations already applied)", c.OpenFGAReplicas)
		return kube.Scale(c.Namespace, "deployment/openfga", c.OpenFGAReplicas, "240s")
	}
	return nil
}

// installVaultESO: OIDC client-secret delivery path — Vault (dev mode) +
// ESO + the manifest-rendered ExternalSecret. Both charts are pinned
// (--version): the CI image cache keys on exact chart/image versions
// (testdata/images.txt), so unpinned charts would silently invalidate it.
func installVaultESO(c *ProvisionConfig) error {
	if err := helm.UpgradeInstall("vault", "hashicorp/vault", "--version", "0.34.1",
		"--namespace", c.Namespace,
		"--set", "server.dev.enabled=true",
		"--set", "server.dev.devRootToken="+c.VaultDevToken,
		"--set", "injector.enabled=false",
		"--wait", "--timeout", "5m"); err != nil {
		return err
	}
	if err := helm.UpgradeInstall("external-secrets", "external-secrets/external-secrets", "--version", "2.11.0",
		"--namespace", "external-secrets", "--create-namespace",
		"--set", "installCRDs=true",
		"--wait", "--timeout", "5m"); err != nil {
		return err
	}
	// helm --wait covers the controller deployments, not CRD establishment;
	// applying a ClusterSecretStore before the API is established fails with
	// "no matches for kind".
	if err := kube.WaitCondition("", "condition=established", "120s",
		"crd/clustersecretstores.external-secrets.io"); err != nil {
		return err
	}
	if err := kube.WaitCondition("", "condition=established", "120s",
		"crd/externalsecrets.external-secrets.io"); err != nil {
		return err
	}
	sec, err := kube.Kubectl("-n", c.Namespace, "create", "secret", "generic", "inari-vault",
		"--from-literal=token="+c.VaultDevToken, "--dry-run=client", "-o", "yaml")
	if err != nil {
		return err
	}
	_, err = kube.ApplyStdin(sec)
	return err
}

// keycloakLifecycle rolls the StatefulSet, pins the issuer hostname (issuer
// consistency — MUST complete before inari-server installs), rolls again.
func keycloakLifecycle(c *ProvisionConfig) error {
	if err := waitCNPG(c.Namespace); err != nil {
		return err
	}
	if err := kube.RolloutStatus(c.Namespace, "statefulset/keycloak", "420s"); err != nil {
		return err
	}
	// Best-effort realm-import job wait (the script tolerated its absence).
	_ = kube.WaitCondition(c.Namespace, "condition=complete", "300s",
		"job", "-l", "app.kubernetes.io/component=keycloak")
	patch := fmt.Sprintf(`{"spec":{"hostname":{"hostname":"http://%s","strict":true}}}`, c.kcFQDN())
	if _, err := kube.Kubectl("-n", c.Namespace, "patch", "keycloak", "keycloak",
		"--type", "merge", "-p", patch); err != nil {
		return err
	}
	return kube.RolloutStatus(c.Namespace, "statefulset/keycloak", "420s")
}

// startToolbox recreates the toolbox pod — the suite's ONLY HTTP transport
// (every curl runs through it, exactly like the script's xcurl).
func startToolbox(c *ProvisionConfig) error {
	_, _ = kube.Kubectl("-n", c.Namespace, "delete", "pod", c.Toolbox,
		"--ignore-not-found", "--wait=false")
	if _, err := kube.Kubectl("-n", c.Namespace, "run", c.Toolbox,
		"--image=curlimages/curl:8.10.1", "--restart=Never",
		`--overrides={"spec":{"securityContext":{"runAsUser":1000}}}`,
		"--command", "--", "sleep", "3600"); err != nil {
		return err
	}
	return kube.WaitCondition(c.Namespace, "condition=ready", "120s", "pod/"+c.Toolbox)
}

// awaitIssuer polls the OIDC discovery document until the issuer converges
// on the service FQDN (no k8s condition exists for issuer state).
func awaitIssuer(c *ProvisionConfig) error {
	client := &kc.Client{NS: c.Namespace, Toolbox: c.Toolbox}
	want := "http://" + c.kcFQDN() + "/realms/inari"
	return poll.Until(120*time.Second, 5*time.Second, func() (bool, error) {
		iss, err := client.RealmIssuer()
		if err != nil {
			return false, nil // transient warm-up: keep polling
		}
		if iss != "" && iss != want {
			logf("issuer not yet converged (got %s, want %s)", iss, want)
		}
		return iss == want, nil
	})
}

// installServer installs the inari-server chart with the e2e images and the
// local git provider hostPath. The optional redis subchart must be vendored
// even when disabled (helm verifies all Chart.yaml dependencies).
func installServer(c *ProvisionConfig) error {
	if err := helm.DependencyBuild(c.ServerChartDir); err != nil {
		return err
	}
	repo, tag := splitImage(c.ServerImage)
	extraEnv := []map[string]string{
		{"name": "INARI_AGENT_GATEWAY_ADDRESS", "value": "http://inari-server." + c.Namespace + ".svc:8080"},
		{"name": "INARI_AGENT_IMAGE_REPO", "value": "inari/agent"},
		{"name": "INARI_GIT_PROVIDER", "value": "local"},
		{"name": "INARI_GIT_LOCAL_ROOT", "value": "/var/lib/inari/git"},
	}
	if c.HA {
		// HA-only scaffold knobs: disruption block HA(a) drives a real
		// scaffold run to prove the claim-based reconcile loop keeps
		// progressing after a pod loss. Never set in non-HA mode.
		extraEnv = append(extraEnv,
			map[string]string{"name": "INARI_SCAFFOLD_TEMPLATE_DIR", "value": "/templates"},
			map[string]string{"name": "INARI_SCAFFOLD_GIT_ORG", "value": "e2e-platform"},
			map[string]string{"name": "INARI_SCAFFOLD_RECONCILE_INTERVAL", "value": "5s"})
	}
	extraEnvJSON, _ := json.Marshal(extraEnv)
	serverValues := "server-values-e2e.yaml"
	if c.HA {
		serverValues = "server-values-e2e-ha.yaml"
	}
	args := []string{
		c.ServerChartDir,
		"--namespace", c.Namespace,
		"-f", testdataFile(serverValues),
		"--set", fmt.Sprintf("replicaCount=%d", c.ServerReplicas),
		"--set", "image.repository=" + repo,
		"--set", "image.tag=" + tag,
		"--set", "image.pullPolicy=IfNotPresent",
		"--set", "keycloak.baseUrl=http://" + c.kcFQDN(),
		"--set", "vault.addr=http://vault." + c.Namespace + ".svc:8200",
		"--set", "nats.enabled=false",
		"--set", "nats.url=nats://nats:4222",
		"--set-json", "extraEnv=" + string(extraEnvJSON),
		"--set-json", `extraVolumes=[{"name":"git-repos","hostPath":{"path":"/git","type":"Directory"}}]`,
		"--set-json", `extraVolumeMounts=[{"name":"git-repos","mountPath":"/var/lib/inari/git"}]`,
		"--wait", "--timeout", "10m",
	}
	if c.CacheBackend == "redis" {
		args = append(args, "--set", "redis.enabled=true", "--set", "cache.backend=redis",
			// e2e-only: the redis subchart (bitnami 28.2.4) defaults to the
			// MUTABLE bitnami/redis:latest tag — unpinnable and uncacheable.
			// Pin the frozen bitnamilegacy repo's newest tag instead
			// (recorded in testdata/images.txt). registry=docker.io is
			// load-bearing: the subchart defaults to registry-1.docker.io,
			// which containerd treats as a DIFFERENT image identity than the
			// preloaded docker.io/bitnamilegacy/redis:8.2.1 — the preload
			// would be ignored and the node would re-pull every run.
			"--set", "redis.image.registry=docker.io",
			"--set", "redis.image.repository=bitnamilegacy/redis",
			"--set", "redis.image.tag=8.2.1")
	}
	if err := helm.UpgradeInstall("inari-server", args...); err != nil {
		return err
	}
	return kube.RolloutStatus(c.Namespace, "deployment/inari-server", "180s")
}

// installOperator: CRDs sibling chart first, then the operator (smoke level
// — full guardrails live in the operator repo).
func installOperator(c *ProvisionConfig) error {
	if err := helm.UpgradeInstall("inari-operator-crds", c.OperatorCRDsChart,
		"--namespace", c.Namespace, "--wait", "--timeout", "3m"); err != nil {
		return err
	}
	if err := helm.UpgradeInstall("inari-operator", c.OperatorChartDir,
		"--namespace", c.Namespace, "--wait", "--timeout", "5m"); err != nil {
		return err
	}
	if err := kube.RolloutStatus(c.Namespace, "deployment/inari-operator", "180s"); err != nil {
		return fmt.Errorf("inari-operator deployment never became ready: %w", err)
	}
	return kube.WaitCondition(c.Namespace, "condition=available", "60s", "deployment/inari-operator")
}

// installConsole: stateless nginx SPA configured for the inari realm (smoke
// level). Ported subtlety: the script verified the route via a host
// port-forward; the provisioner curls the service DNS name from the toolbox
// pod instead — same assertions (SPA index + config.js contents), no
// port-forward fragility, consistent with every other HTTP call.
func installConsole(c *ProvisionConfig) error {
	if err := helm.UpgradeInstall("inari-console", c.ConsoleChartDir,
		"--namespace", c.Namespace,
		"--set", "keycloakUrl=http://"+c.kcFQDN(),
		"--set", "keycloakRealm=inari",
		"--set", "nginx.corsOrigins={http://"+c.kcFQDN()+"}",
		"--wait", "--timeout", "5m"); err != nil {
		return err
	}
	if err := kube.RolloutStatus(c.Namespace, "deployment/inari-console", "180s"); err != nil {
		return fmt.Errorf("inari-console deployment never became ready: %w", err)
	}
	if err := kube.WaitCondition(c.Namespace, "condition=available", "60s", "deployment/inari-console"); err != nil {
		return err
	}
	index, err := kube.Curl(c.Namespace, c.Toolbox, "http://inari-console/")
	if err != nil || !strings.Contains(strings.ToLower(index), "<html") {
		return fmt.Errorf("console route did not serve the SPA index.html (got: %.120s)", index)
	}
	config, err := kube.Curl(c.Namespace, c.Toolbox, "http://inari-console/config.js")
	if err != nil {
		return err
	}
	// B10 (run d701e2ba): pin realm, client id, and API base URL too — a
	// wrong value strands the SPA at login or points it at a dead API.
	for _, want := range []string{
		`keycloakUrl: "http://` + c.kcFQDN() + `"`,
		`keycloakRealm: "inari"`,
		`keycloakClientId: "inari-ui"`,
		`apiBaseUrl: "/api/v1"`,
	} {
		if !strings.Contains(config, want) {
			return fmt.Errorf("console config.js missing %q: %s", want, config)
		}
	}
	logf("console serves the SPA and its config targets http://%s (realm inari, client inari-ui, api /api/v1)", c.kcFQDN())
	return nil
}

// awaitOutboxStream polls until the server ensures the INARI_OUTBOX stream
// at boot (ADR-0014; app-level, no k8s condition). Replicas match the leg:
// R=3 on HA (3-node JetStream), R=1 on the trimmed non-HA single node.
//
// Diagnostics hardening (Oct 2026 postmortem): a bare "stream never formed"
// hid two very different root causes — the probe itself broken (nats-box
// exec erroring, indistinguishable from a missing stream because exec
// errors were swallowed) and the deployed server image predating ADR-0014
// (it never creates the stream). Track the last probe error and the last
// mismatch detail, and surface both in the timeout message. The
// stream-not-found exec error is the ONLY expected miss; any other probe
// error is recorded verbatim.
func awaitOutboxStream(c *ProvisionConfig) error {
	wantReplicas := 1
	if c.HA {
		wantReplicas = 3
	}
	var lastProbeErr error
	var lastDetail string
	err := poll.Until(120*time.Second, 5*time.Second, func() (bool, error) {
		out, err := kube.ExecInPod(c.Namespace, "deploy/nats-box",
			"nats", "stream", "info", "INARI_OUTBOX", "--server", "nats:4222", "--json")
		if err != nil {
			if isStreamNotFound(err) {
				lastDetail = "stream does not exist yet"
			} else {
				// Probe itself failed (nats-box not ready, CLI/auth
				// regression, …): remember it — if every attempt fails this
				// way the timeout message must say so instead of implying
				// the server never created the stream.
				lastProbeErr = err
			}
			return false, nil
		}
		formed, detail := parseOutboxStreamInfo(out, wantReplicas)
		lastDetail = detail
		return formed, nil
	})
	if err == nil {
		return nil
	}
	msg := fmt.Sprintf("%v (last state: %s)", err, lastDetail)
	if lastProbeErr != nil {
		msg += fmt.Sprintf("; last probe exec error: %v", lastProbeErr)
	}
	msg += ". If the deployed server image predates ADR-0014 it never creates " +
		"the stream — check `kubectl -n " + c.Namespace + " logs deploy/inari-server` " +
		"for eventbus/NATS lines and verify the image tag matches the source under test"
	return fmt.Errorf("%s", msg)
}

// parseOutboxStreamInfo interprets one `nats stream info --json` payload:
// formed only when the replica count matches the leg AND the outbox subject
// filter is present. The detail string always describes what was seen, so a
// mismatch (or garbage output) is diagnosable from the failure message.
func parseOutboxStreamInfo(out string, wantReplicas int) (bool, string) {
	var doc struct {
		Config struct {
			Replicas int      `json:"num_replicas"`
			Subjects []string `json:"subjects"`
		} `json:"config"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		return false, fmt.Sprintf("unparseable stream info: %.120s", out)
	}
	subjectOK := false
	for _, s := range doc.Config.Subjects {
		if s == "inari.outbox.>" {
			subjectOK = true
		}
	}
	if doc.Config.Replicas != wantReplicas {
		return false, fmt.Sprintf("stream exists with num_replicas=%d (want %d), subjects=%v",
			doc.Config.Replicas, wantReplicas, doc.Config.Subjects)
	}
	if !subjectOK {
		return false, fmt.Sprintf("stream exists with num_replicas=%d but missing subject inari.outbox.> (subjects=%v)",
			doc.Config.Replicas, doc.Config.Subjects)
	}
	return true, fmt.Sprintf("stream formed (num_replicas=%d, subjects=%v)", doc.Config.Replicas, doc.Config.Subjects)
}

// isStreamNotFound reports whether an exec error is the nats CLI's
// stream-not-found — the expected state while the server has not ensured
// the stream yet. Any other exec error means the PROBE is broken.
//
// Two shapes are recognized, both verified against the pinned probe image
// (natsio/nats-box:0.17.0, nats CLI v0.2.0):
//   - "stream not found (10059)" — the lookup failure some CLI versions
//     print directly;
//   - "could not pick a Stream to operate on" — what nats-box:0.17.0
//     actually prints for a missing stream (the CLI falls back to
//     interactive stream selection after the lookup fails, then aborts
//     because kubectl exec has no terminal).
func isStreamNotFound(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "stream not found") ||
		strings.Contains(msg, "could not pick a Stream")
}

// ProvisionStack is TestProvisionStack: every helm install/upgrade of the
// golden path, in dependency layers (see the package comment graph). The
// script (phase 2) called this between kind setup and KC seeding; phase 3
// calls it from the in-process provisioner (Provision).
func ProvisionStack(t *testing.T) {
	provisionCharts(t, LoadProvisionConfig(t))
}

// provisionCharts installs every chart of the platform stack against an
// EXISTING kind cluster, in dependency layers. Ends with the toolbox pod
// running, the Keycloak issuer converged, the console smoke-verified, and
// the INARI_OUTBOX stream formed.
func provisionCharts(t *testing.T, c *ProvisionConfig) {
	logf("adding helm repos (serial; component installs run concurrently below)")
	if err := helmRepoSetup(); err != nil {
		t.Fatalf("helm repo setup: %v", err)
	}

	// Pre-create the stack namespace so the concurrent layer-A installs
	// (nats/vault use --namespace without --create-namespace) never race
	// platform-config's --create-namespace.
	if err := kube.EnsureNamespace(c.Namespace); err != nil {
		t.Fatalf("ensuring namespace %s: %v", c.Namespace, err)
	}

	// Layer A: platform-config (DB + Keycloak realm) ∥ NATS ∥ Vault/ESO —
	// none of them depend on each other (dependency graph above).
	logf("installing platform-config ∥ nats ∥ vault+eso (concurrent layer A)")
	var layerA errgroup.Group
	layerA.Go(func() error { return installPlatformConfig(c) })
	layerA.Go(func() error { return installNATS(c) })
	layerA.Go(func() error { return installVaultESO(c) })
	if err := layerA.Wait(); err != nil {
		t.Fatalf("layer A install failed: %v (logs: kubectl -n %s get pods)", err, c.Namespace)
	}

	// Toolbox comes up as soon as the namespace exists — the issuer poll
	// and the script's KC seeding both ride on it.
	if err := startToolbox(c); err != nil {
		t.Fatalf("starting toolbox pod: %v", err)
	}

	// Layer B: Keycloak rollout/patch/rollout (long pole) ∥ OpenFGA — both
	// derive from the platform database, neither depends on the other.
	logf("keycloak lifecycle ∥ openfga (concurrent layer B)")
	var layerB errgroup.Group
	layerB.Go(func() error { return keycloakLifecycle(c) })
	layerB.Go(func() error { return installOpenFGA(c) })
	if err := layerB.Wait(); err != nil {
		t.Fatalf("layer B install failed: %v (logs: kubectl -n %s get pods)", err, c.Namespace)
	}

	if err := awaitIssuer(c); err != nil {
		t.Fatalf("issuer did not converge to %s: %v", c.kcFQDN(), err)
	}

	// Barrier: the server requires the Keycloak issuer patch, NATS,
	// OpenFGA, and Vault/ESO all in place.
	logf("installing inari-server chart (e2e image, cache backend: %s)", c.CacheBackend)
	if err := installServer(c); err != nil {
		t.Fatalf("inari-server install: %v", err)
	}

	// Layer C: operator (crds first) ∥ console — smoke level, independent
	// of each other.
	logf("installing inari-operator ∥ inari-console (concurrent layer C, smoke)")
	var layerC errgroup.Group
	layerC.Go(func() error { return installOperator(c) })
	layerC.Go(func() error { return installConsole(c) })
	if err := layerC.Wait(); err != nil {
		t.Fatalf("layer C install failed: %v", err)
	}

	for _, release := range []string{"inari-operator", "inari-console"} {
		ok, err := helm.Installed(c.Namespace, release)
		if err != nil || !ok {
			t.Fatalf("helm release %s missing in namespace %s", release, c.Namespace)
		}
	}

	if err := awaitOutboxStream(c); err != nil {
		t.Fatalf("INARI_OUTBOX stream never formed (ha=%v): %v "+
			"(logs: kubectl -n %s logs statefulset/nats)", c.HA, err, c.Namespace)
	}
	logf("PASS: golden-path charts installed (ha=%v)", c.HA)
}

// AgentInput is the hand-off the KC seeding/registration stage (script in
// phase 2, in-process in phase 3) writes for TestProvisionAgent: everything
// the agent chart install needs that only exists after cluster
// registration.
type AgentInput struct {
	Namespace     string `json:"namespace"`
	ClusterID     string `json:"cluster_id"`
	OrgID         string `json:"org_id"` // agent tenant id
	RegToken      string `json:"reg_token"`
	AgentChartDir string `json:"agent_chart_dir"`
	AgentImage    string `json:"agent_image"`
	VaultDevToken string `json:"vault_dev_token"`
}

// ProvisionAgent is TestProvisionAgent: the inari-agent helm install plus
// the ESO wiring, driven by the agent-input JSON the registration stage
// wrote ($E2E_AGENT_INPUT_PATH).
func ProvisionAgent(t *testing.T) {
	path := os.Getenv("E2E_AGENT_INPUT_PATH")
	if path == "" {
		t.Fatal("E2E_AGENT_INPUT_PATH not set: the registration stage must write the agent input JSON")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading agent input %s: %v", path, err)
	}
	var in AgentInput
	if err := json.Unmarshal(raw, &in); err != nil {
		t.Fatalf("parsing agent input %s: %v", path, err)
	}
	provisionAgent(t, in)
}

// provisionAgent installs the inari-agent chart and wires ESO (Vault token
// secret + ClusterSecretStore). Split from provisionCharts because the
// registration token only exists after tenant + cluster registration
// against the running server.
func provisionAgent(t *testing.T, in AgentInput) {
	for field, v := range map[string]string{
		"namespace": in.Namespace, "cluster_id": in.ClusterID, "org_id": in.OrgID,
		"reg_token": in.RegToken, "agent_chart_dir": in.AgentChartDir, "agent_image": in.AgentImage,
		"vault_dev_token": in.VaultDevToken,
	} {
		if v == "" {
			t.Fatalf("agent input: field %q is empty", field)
		}
	}

	// ESO wiring comes FIRST: `helm --wait` blocks on agent readiness, which
	// requires the ESO-synced OIDC client secret, which requires the
	// inari-platform ClusterSecretStore and its Vault token secret to
	// already exist. Installing the chart first deadlocked deterministically
	// (helm wait expired at 180s with the agent stuck waiting for the ESO
	// secret; the store was never applied because the install never
	// returned) — the Go port's original order was only safe for ESO CRD
	// retries, not for a blocking --wait. The token secret lives in the
	// always-present default namespace: pre-creating the chart-owned
	// inari-system namespace makes helm refuse the install (missing
	// release ownership metadata).
	sec, err := kube.Kubectl("-n", "default", "create", "secret", "generic", "inari-vault-token",
		"--from-literal=token="+in.VaultDevToken, "--dry-run=client", "-o", "yaml")
	if err != nil {
		t.Fatalf("rendering inari-vault-token secret: %v", err)
	}
	if _, err := kube.ApplyStdin(sec); err != nil {
		t.Fatalf("applying inari-vault-token secret: %v", err)
	}
	store := fmt.Sprintf(`apiVersion: external-secrets.io/v1
kind: ClusterSecretStore
metadata:
  name: inari-platform
spec:
  provider:
    vault:
      server: http://vault.%s.svc:8200
      path: secret
      version: v2
      auth:
        tokenSecretRef:
          name: inari-vault-token
          namespace: default
          key: token
`, in.Namespace)
	if _, err := kube.ApplyStdin(store); err != nil {
		t.Fatalf("applying inari-platform ClusterSecretStore: %v", err)
	}

	repo, tag := splitImage(in.AgentImage)
	// --namespace default: the chart renders and owns the inari-system
	// Namespace itself, so the release namespace is irrelevant — but it must
	// be pinned explicitly (in-cluster runners would otherwise resolve the
	// pod's serviceaccount namespace).
	// oidcSecret.remotePath: the control plane writes the OIDC client secret
	// at the trimmed Vault path (secrets.ClusterOIDCPath strips the
	// "cluster:" type prefix from the cluster ID).
	err = helm.UpgradeInstall("inari-agent", in.AgentChartDir,
		"--namespace", "default",
		"--set", "image.repository="+repo,
		"--set", "image.tag="+tag,
		"--set", "image.pullPolicy=IfNotPresent",
		"--set", "config.tenantID="+in.OrgID,
		"--set", "config.controlPlane=http://inari-server."+in.Namespace+".svc:8080",
		"--set", "config.registrationToken="+in.RegToken,
		"--set", "config.clusterLabels=e2e=true",
		// kubectlTunnel defaults on, but kubeproxyURL is empty by default
		// and the tunnel-agent fails fast without it, so helm --wait can
		// never succeed. The golden path asserts the agent chain only; the
		// tunnel chain is covered by the dedicated kubectl-tunnel e2e
		// (e2e-nightly). Disable it here.
		"--set", "kubectlTunnel.enabled=false",
		"--set", "oidcSecret.create=true",
		"--set", "oidcSecret.secretStore=inari-platform",
		"--set", "oidcSecret.remotePath=inari/clusters/"+strings.TrimPrefix(in.ClusterID, "cluster:")+"/oidc-client-secret",
		"--wait", "--timeout", "180s")
	if err != nil {
		t.Fatalf("inari-agent install: %v", err)
	}
	logf("PASS: inari-agent installed and ESO wired (cluster %s)", in.ClusterID)
}
