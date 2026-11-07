//go:build e2e

package kube

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRenderInClusterKubeconfig(t *testing.T) {
	got := renderInClusterKubeconfig("10.43.0.1", "443")
	for _, want := range []string{
		"server: https://10.43.0.1:443",
		// tokenFile (not an embedded token): BoundServiceAccountTokenVolume
		// rotates the SA token mid-run — the file reference survives that.
		"tokenFile: /var/run/secrets/kubernetes.io/serviceaccount/token",
		"certificate-authority: /var/run/secrets/kubernetes.io/serviceaccount/ca.crt",
		"current-context: in-cluster",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("kubeconfig missing %q:\n%s", want, got)
		}
	}
}

func TestKubeconfigEnvDecision(t *testing.T) {
	// An explicit KUBECONFIG always wins — dev attach runs are untouched.
	t.Setenv("KUBECONFIG", "/home/dev/.kube/config")
	t.Setenv("KUBERNETES_SERVICE_HOST", "10.43.0.1")
	if got := kubeconfigEnv(); got != "" {
		t.Fatalf("kubeconfigEnv = %q, want empty (explicit KUBECONFIG wins)", got)
	}
	// No KUBECONFIG, no ~/.kube/config, in-cluster env present, and the SA
	// token readable → render. This test environment has no SA token, so the
	// result must be empty (dev laptop behavior) even with the env vars set.
	t.Setenv("KUBECONFIG", "")
	home := t.TempDir()
	t.Setenv("HOME", home)
	if got := kubeconfigEnv(); got != "" {
		t.Fatalf("kubeconfigEnv = %q, want empty without an SA token", got)
	}
	// A ~/.kube/config under the home dir suppresses the render too.
	if err := os.MkdirAll(filepath.Join(home, ".kube"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".kube", "config"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := kubeconfigEnv(); got != "" {
		t.Fatalf("kubeconfigEnv = %q, want empty with ~/.kube/config present", got)
	}
}
