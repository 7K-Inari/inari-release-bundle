// Package kube is a thin exec wrapper over the kubectl CLI — parity with
// chart-testing (no client-go): there is zero magic between the test and
// the CLI behavior, and failures surface the exact command + stderr.
package kube

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

// kubectl with impersonation flags (auth can-i --as/--as-group — the RBAC
// phase's authorizer probe) does NOT fall back to the in-cluster config: it
// dies on localhost:8080 even though plain kubectl in the same pod resolves
// the in-cluster endpoint fine (verified live, e2e-stack-mvhhf, 2026-11-07).
// Fix: in-cluster runs (SA token + KUBERNETES_SERVICE_HOST, no KUBECONFIG,
// no ~/.kube/config) get an explicit kubeconfig rendered from the SA files.
// Dev hosts and explicit KUBECONFIG runs are untouched.
const saDir = "/var/run/secrets/kubernetes.io/serviceaccount"

var (
	writeKubeconfigOnce sync.Once
	writtenKubeconfig   string
)

// renderInClusterKubeconfig renders a kubeconfig referencing the SA token
// by FILE (tokenFile, not an embedded value): BoundServiceAccountTokenVolume
// rotates the token mid-run and the file reference survives that.
func renderInClusterKubeconfig(host, port string) string {
	return `apiVersion: v1
kind: Config
clusters:
- name: in-cluster
  cluster:
    server: https://` + host + `:` + port + `
    certificate-authority: ` + saDir + `/ca.crt
contexts:
- name: in-cluster
  context:
    cluster: in-cluster
    user: sa
current-context: in-cluster
users:
- name: sa
  user:
    tokenFile: ` + saDir + `/token
`
}

// kubeconfigEnv returns the "KUBECONFIG=<path>" env entry for child
// processes, or "" when the ambient resolution should be used.
func kubeconfigEnv() string {
	if os.Getenv("KUBECONFIG") != "" {
		return ""
	}
	if home, err := os.UserHomeDir(); err == nil {
		if _, err := os.Stat(filepath.Join(home, ".kube", "config")); err == nil {
			return ""
		}
	}
	host := os.Getenv("KUBERNETES_SERVICE_HOST")
	if host == "" {
		return ""
	}
	port := os.Getenv("KUBERNETES_SERVICE_PORT")
	if port == "" {
		port = "443"
	}
	if _, err := os.Stat(saDir + "/token"); err != nil {
		return ""
	}
	writeKubeconfigOnce.Do(func() {
		path := filepath.Join(os.TempDir(), "e2e-incluster-kubeconfig")
		if err := os.WriteFile(path, []byte(renderInClusterKubeconfig(host, port)), 0o600); err == nil {
			writtenKubeconfig = path
		}
	})
	if writtenKubeconfig == "" {
		return ""
	}
	return "KUBECONFIG=" + writtenKubeconfig
}

// withKubeconfigEnv applies kubeconfigEnv to a command's environment.
func withKubeconfigEnv(cmd *exec.Cmd) {
	if ke := kubeconfigEnv(); ke != "" {
		cmd.Env = append(os.Environ(), ke)
	}
}

// Run executes a CLI and returns stdout; stderr is attached to the error.
func Run(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	withKubeconfigEnv(cmd)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%s %s: %w\n%s", name, strings.Join(args, " "), err, stderr.String())
	}
	return stdout.String(), nil
}

// RunStdin executes a CLI with stdin piped in; stderr is attached to the error.
func RunStdin(name, stdin string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	withKubeconfigEnv(cmd)
	cmd.Stdin = strings.NewReader(stdin)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%s %s: %w\n%s", name, strings.Join(args, " "), err, stderr.String())
	}
	return stdout.String(), nil
}

// Kubectl runs kubectl with the given args.
func Kubectl(args ...string) (string, error) { return Run("kubectl", args...) }

// ExecInPod runs cmd inside a pod (no shell implied — pass sh -c yourself).
func ExecInPod(ns, pod string, cmd ...string) (string, error) {
	args := []string{"-n", ns, "exec", pod, "--"}
	return Kubectl(append(args, cmd...)...)
}

// ExecInContainer runs cmd inside a specific container of a pod.
func ExecInContainer(ns, pod, container string, cmd ...string) (string, error) {
	args := []string{"-n", ns, "exec", pod, "-c", container, "--"}
	return Kubectl(append(args, cmd...)...)
}

// ExecInPodStdin runs cmd inside a pod with stdin piped in.
func ExecInPodStdin(ns, pod, stdin string, cmd ...string) (string, error) {
	args := []string{"-n", ns, "exec", "-i", pod, "--"}
	args = append(args, cmd...)
	c := exec.Command("kubectl", args...)
	withKubeconfigEnv(c)
	c.Stdin = strings.NewReader(stdin)
	var stdout, stderr bytes.Buffer
	c.Stdout = &stdout
	c.Stderr = &stderr
	if err := c.Run(); err != nil {
		return "", fmt.Errorf("kubectl %s: %w\n%s", strings.Join(args, " "), err, stderr.String())
	}
	return stdout.String(), nil
}

// Curl mirrors the script's xcurl: curl -sf -m 20 inside the toolbox pod.
// Every HTTP call in the suite goes through this so the suite is immune to
// port-forward fragility, exactly like golden-path.sh.
func Curl(ns, toolbox string, args ...string) (string, error) {
	full := append([]string{"curl", "-sf", "-m", "20"}, args...)
	return ExecInPod(ns, toolbox, full...)
}

// RolloutStatus waits for a rollout (deployment/statefulset name form).
func RolloutStatus(ns, resource, timeout string) error {
	_, err := Kubectl("-n", ns, "rollout", "status", resource, "--timeout="+timeout)
	return err
}

// WaitCondition wraps kubectl wait --for=<condition> <resources...>. An
// empty ns targets cluster-scoped resources (no -n flag).
func WaitCondition(ns, condition, timeout string, resources ...string) error {
	var args []string
	if ns != "" {
		args = append(args, "-n", ns)
	}
	args = append(args, "wait", "--for="+condition)
	args = append(args, resources...)
	args = append(args, "--timeout="+timeout)
	_, err := Kubectl(args...)
	return err
}

// ApplyStdin pipes a manifest into kubectl apply -f -.
func ApplyStdin(manifest string) (string, error) {
	c := exec.Command("kubectl", "apply", "-f", "-")
	withKubeconfigEnv(c)
	c.Stdin = strings.NewReader(manifest)
	var stdout, stderr bytes.Buffer
	c.Stdout = &stdout
	c.Stderr = &stderr
	if err := c.Run(); err != nil {
		return "", fmt.Errorf("kubectl apply -f -: %w\n%s", err, stderr.String())
	}
	return stdout.String(), nil
}

// EnsureNamespace creates the namespace if absent (idempotent).
func EnsureNamespace(ns string) error {
	out, err := Kubectl("create", "namespace", ns, "--dry-run=client", "-o", "yaml")
	if err != nil {
		return err
	}
	_, err = ApplyStdin(out)
	return err
}

// Logs returns the full logs (--tail=-1) for a label selector.
func Logs(ns, selector string) (string, error) {
	return Kubectl("-n", ns, "logs", "-l", selector, "--tail=-1")
}

// Scale sets replicas and waits for the rollout.
func Scale(ns, resource string, replicas int, timeout string) error {
	if _, err := Kubectl("-n", ns, "scale", resource, fmt.Sprintf("--replicas=%d", replicas)); err != nil {
		return err
	}
	return RolloutStatus(ns, resource, timeout)
}

// PSQLInari runs SQL against the platform database via the CNPG primary pod
// (the connection never leaves the cluster). Preferred path: pod-local auth
// as the postgres superuser — CNPG images peer/trust local connections from
// the container's own user, and kubectl exec lands as that user. This needs
// NO secret access, so least-privilege runners (the live env's e2e-runner
// can exec but must not read inari-db) can query — the same posture as the
// chainsaw bootstrap probes (gitops/e2e/argo-workflows/rbac.yaml). Fallback:
// the inari-db secret's URI, for environments where pod-local auth is
// disabled.
func PSQLInari(ns, sql string) (string, error) {
	primary, err := Kubectl("-n", ns, "get", "cluster.postgresql.cnpg.io/postgresql",
		"-o", "jsonpath={.status.currentPrimary}")
	if err != nil {
		return "", err
	}
	pod := strings.TrimSpace(primary)
	out, err := ExecInContainer(ns, pod, "postgres",
		"psql", "-U", "postgres", "-d", "inari", "-tAc", sql)
	if err == nil {
		return strings.TrimSpace(out), nil
	}
	uriB64, serr := Kubectl("-n", ns, "get", "secret", "inari-db",
		"-o", "jsonpath={.data.inari-uri}")
	if serr != nil {
		return "", err // report the primary (peer-auth) failure
	}
	uri, derr := base64.StdEncoding.DecodeString(strings.TrimSpace(uriB64))
	if derr != nil {
		return "", fmt.Errorf("decoding inari-db uri: %w", derr)
	}
	out, err = ExecInContainer(ns, pod, "postgres", "psql", string(uri), "-tAc", sql)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// AuthCanI mirrors `kubectl auth can-i <verb> <resource> --as=<user>
// --as-group=<g>`: exit 0 means allowed, exit 1 with "no" on stdout means
// denied; anything else is a real error.
func AuthCanI(user string, groups []string, verb, resource string) (bool, error) {
	args := []string{"auth", "can-i", verb, resource, "--as=" + user}
	for _, g := range groups {
		args = append(args, "--as-group="+g)
	}
	cmd := exec.Command("kubectl", args...)
	withKubeconfigEnv(cmd)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err == nil {
		return true, nil
	}
	if strings.TrimSpace(stdout.String()) == "no" {
		return false, nil
	}
	return false, fmt.Errorf("kubectl %s: %w\n%s", strings.Join(args, " "), err, stderr.String())
}
