//go:build e2e

// Full in-process golden-path provisioning — phase 3 of the shell→Go
// migration: kind cluster lifecycle, the git-root hostPath, image load, the
// operators prereq script, every helm install (provisionCharts), and the KC
// seeding + tenant/cluster registration (seed.go). Replaces
// scripts/e2e/golden-path.sh, which is deleted in this phase.
package suite

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"7k-inari/inari-release-bundle/scripts/e2e/stack/internal/kind"
	"7k-inari/inari-release-bundle/scripts/e2e/stack/internal/kube"
)

// defaultKindNodeImage pins the kind node image so CI can preload and cache
// it (scripts/e2e/stack/testdata/images.txt — bump there AND here). Matches
// the kind v0.26.0 default (helm/kind-action@v1.12.0 installs kind v0.26.0).
// Override with KIND_NODE_IMAGE; empty disables the pin (kind default).
const defaultKindNodeImage = "kindest/node:v1.32.0@sha256:c48c62eac5da28cdadcf560d1d8616cfa6783b58f0d94cf63ad1bf49600cb027"

// kindConfigYAML renders the kind cluster config: a single control-plane
// node with the host git root mounted at /git, optionally pinned to a node
// image (empty nodeImage = kind's built-in default).
func kindConfigYAML(gitHostDir, nodeImage string) string {
	imageLine := ""
	if nodeImage != "" {
		imageLine = "    image: " + nodeImage + "\n"
	}
	return fmt.Sprintf(`kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
nodes:
  - role: control-plane
%s    extraMounts:
      - hostPath: %s
        containerPath: /git
`, imageLine, gitHostDir)
}

// Provision brings the whole golden-path stack up from nothing and returns
// the suite Env. Cleanup parity with the script: KEEP_CLUSTER=true leaves
// the cluster and git host dir for inspection; otherwise both are torn down
// at test end. When E2E_HANDOFF_PATH is set the handoff JSON is written
// there (and the git host dir survives either way — later CI steps and
// local reruns consume materialized state repos from it).
func Provision(t TB) *Env {
	t.Helper()
	c := LoadProvisionConfig(t)

	// Host-side git root for INARI_GIT_PROVIDER=local: the server writes
	// real bare repos here (mounted into the kind node), and the suite
	// clones and applies baseline/rbac/ from them — GAP(rbac-e2e-argocd):
	// stands in for the tenant-local ArgoCD the e2e platform stack does not
	// install. mktemp dirs are 0700; the server container runs non-root, so
	// open the shared git root up.
	// GIT_HOST_DIR pins the git root to a fixed path — CI pre-creates the
	// cluster with that path already mounted (image preload happens before
	// the suite runs), so the suite must reuse it instead of a mktemp dir.
	gitHostDir := c.GitHostDir
	if gitHostDir != "" {
		if err := os.MkdirAll(gitHostDir, 0o777); err != nil {
			t.Fatalf("creating git host dir: %v", err)
		}
	} else {
		var err error
		gitHostDir, err = os.MkdirTemp("/tmp", "inari-e2e-git.")
		if err != nil {
			t.Fatalf("creating git host dir: %v", err)
		}
	}
	if err := os.Chmod(gitHostDir, 0o777); err != nil {
		t.Fatalf("chmod git host dir: %v", err)
	}

	handoffPath := os.Getenv("E2E_HANDOFF_PATH")
	t.Cleanup(func() {
		// The toolbox pod dies with the kind cluster either way.
		if !c.KeepCluster {
			if err := kind.Delete(c.ClusterName); err != nil {
				fmt.Printf("[e2e] WARN: kind delete cluster %s: %v\n", c.ClusterName, err)
			}
			if handoffPath == "" {
				_ = os.RemoveAll(gitHostDir)
			}
		}
	})

	// extraMounts: the host git root lands at /git inside the kind node so
	// the server pod can hostPath-mount it for the local git provider.
	// E2E_ADOPT_CLUSTER=1: reuse a cluster the workflow already created and
	// preloaded with cached images (CI fast path); a missing cluster is
	// still created as usual so local runs keep working.
	if c.AdoptCluster {
		exists, err := kind.ClusterExists(c.ClusterName)
		if err != nil {
			t.Fatalf("checking kind cluster %q: %v", c.ClusterName, err)
		}
		if exists {
			logf("adopting pre-created kind cluster %q (E2E_ADOPT_CLUSTER=1)", c.ClusterName)
		} else {
			logf("E2E_ADOPT_CLUSTER=1 but cluster %q missing — creating it", c.ClusterName)
			if err := kind.Recreate(c.ClusterName, kindConfigYAML(gitHostDir, c.KindNodeImage)); err != nil {
				t.Fatalf("kind create cluster: %v", err)
			}
		}
	} else {
		logf("creating kind cluster %q", c.ClusterName)
		if err := kind.Recreate(c.ClusterName, kindConfigYAML(gitHostDir, c.KindNodeImage)); err != nil {
			t.Fatalf("kind create cluster: %v", err)
		}
	}
	if _, err := kube.Kubectl("config", "use-context", "kind-"+c.ClusterName); err != nil {
		t.Fatalf("switching kube context: %v", err)
	}

	logf("loading images (%s, %s)", c.ServerImage, c.AgentImage)
	if err := kind.LoadImages(c.ClusterName, c.ServerImage, c.AgentImage); err != nil {
		t.Fatalf("kind load images: %v", err)
	}

	// install-operators.sh STAYS shell — it is a test asset shared with
	// local e2e runs; the provisioner execs it rather than absorbing it.
	logf("installing prerequisite operators (CNPG + Keycloak — the charts never install operators)")
	if _, err := kube.Run("bash", filepath.Join(c.RepoRoot, "scripts/install-operators.sh")); err != nil {
		t.Fatalf("install-operators.sh: %v", err)
	}

	provisionCharts(t, c)

	h := Handoff{
		Namespace:     c.Namespace,
		Toolbox:       c.Toolbox,
		Tenant:        c.Tenant,
		GitHostDir:    gitHostDir,
		AgentChartDir: c.AgentChartDir,
	}
	e := newEnv(h)
	seed, clusterID, orgID := seedKeycloakAndTenant(t, e, c)
	e.KCUID = seed.KCUID
	e.OrgKCID = seed.OrgKCID
	e.ClusterID = clusterID
	e.OrgID = orgID

	if handoffPath != "" {
		raw, err := json.MarshalIndent(e.Handoff, "", "  ")
		if err != nil {
			t.Fatalf("marshaling handoff: %v", err)
		}
		if err := os.WriteFile(handoffPath, raw, 0o644); err != nil {
			t.Fatalf("writing handoff %s: %v", handoffPath, err)
		}
		logf("handoff written to %s", handoffPath)
	}
	logf("PASS: golden-path stack provisioned (ha=%v, tenant=%s, cluster=%s)",
		e.HA, e.Tenant, e.ClusterID)
	return e
}
