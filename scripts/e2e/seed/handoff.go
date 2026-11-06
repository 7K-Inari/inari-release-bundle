// Handoff publishing: the seed resolves the discovered values (KC user id,
// org ids, cluster id — the kind-era Handoff struct fields) once per run and
// publishes them as the e2e-handoff ConfigMap the Ginkgo suite reads via
// E2E_HANDOFF_CONFIGMAP (stack/suite/env.go). The ConfigMap key set is a
// contract with that reader — keep in sync (handoffKeys pins it in tests).
//
// Kubernetes access without client-go: in-cluster the SA token + CA drive
// stdlib HTTPS against the API server; with E2E_KUBECONFIG set (local attach
// to the live env) the seed shells out to kubectl instead. Three verbs
// (ConfigMap get/create/patch, pod get/create, secret get) don't justify
// client-go's module weight; if the k8s surface grows, switch.
package main

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Handoff keys — the exact set stack/suite/env.go's loadHandoffConfigMap
// requires (git_host_dir/agent_chart_dir are kind-era and legitimately
// absent on the live env).
var handoffKeys = []string{"namespace", "toolbox", "tenant", "kc_uid", "org_kc_id", "cluster_id", "org_id"}

// kubeStore is the seed's Kubernetes write surface.
type kubeStore interface {
	// WriteConfigMap creates or merge-patches the ConfigMap's data.
	WriteConfigMap(ns, name string, data map[string]string) error
	// EnsureToolbox makes a curl-capable pod exist and waits for Ready.
	EnsureToolbox(ns, name, image string) error
	// ReadSecretKey reads one key of a secret (fallback for the KC admin
	// client secret when SEED_KC_ADMIN_CLIENT_SECRET is unset).
	ReadSecretKey(ns, name, key string) (string, error)
}

// newKubeStore picks the transport: SA REST in-cluster, kubectl when a
// kubeconfig is available, error otherwise.
func newKubeStore() (kubeStore, error) {
	if _, err := os.Stat(serviceAccountTokenPath); err == nil {
		return newSAStore()
	}
	if os.Getenv("E2E_KUBECONFIG") != "" || os.Getenv("KUBECONFIG") != "" || defaultKubeconfigExists() {
		return kubectlStore{}, nil
	}
	return nil, fmt.Errorf("no Kubernetes access: not in-cluster (no service account) and no kubeconfig (set E2E_KUBECONFIG)")
}

const saDir = "/var/run/secrets/kubernetes.io/serviceaccount"

var serviceAccountTokenPath = saDir + "/token"

func defaultKubeconfigExists() bool {
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	_, err = os.Stat(home + "/.kube/config")
	return err == nil
}

// --- in-cluster SA REST store ------------------------------------------------

type saStore struct {
	base  string
	token string
	http  *http.Client
}

func newSAStore() (*saStore, error) {
	token, err := os.ReadFile(serviceAccountTokenPath)
	if err != nil {
		return nil, err
	}
	ca, err := os.ReadFile(saDir + "/ca.crt")
	if err != nil {
		return nil, fmt.Errorf("reading SA CA: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(ca) {
		return nil, fmt.Errorf("SA ca.crt carried no certificates")
	}
	host := os.Getenv("KUBERNETES_SERVICE_HOST")
	port := os.Getenv("KUBERNETES_SERVICE_PORT")
	if host == "" {
		return nil, fmt.Errorf("KUBERNETES_SERVICE_HOST unset (not in-cluster?)")
	}
	return &saStore{
		base:  "https://" + host + ":" + port,
		token: strings.TrimSpace(string(token)),
		http: &http.Client{
			Timeout:   30 * time.Second,
			Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}},
		},
	}, nil
}

func (s *saStore) do(method, path string, body any) (int, []byte, error) {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, s.base+path, reader)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+s.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if method == "PATCH" {
		req.Header.Set("Content-Type", "application/merge-patch+json")
	}
	res, err := s.http.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		return 0, nil, err
	}
	if res.StatusCode >= 400 && res.StatusCode != 404 {
		return res.StatusCode, nil, fmt.Errorf("%s %s -> %d: %s", method, path, res.StatusCode, truncate(string(raw), 300))
	}
	return res.StatusCode, raw, nil
}

func (s *saStore) WriteConfigMap(ns, name string, data map[string]string) error {
	path := "/api/v1/namespaces/" + ns + "/configmaps/" + name
	status, _, err := s.do("GET", path, nil)
	if err != nil {
		return err
	}
	if status == 404 {
		_, _, err = s.do("POST", "/api/v1/namespaces/"+ns+"/configmaps", map[string]any{
			"apiVersion": "v1",
			"kind":       "ConfigMap",
			"metadata":   map[string]any{"name": name, "namespace": ns},
			"data":       data,
		})
		return err
	}
	_, _, err = s.do("PATCH", path, map[string]any{"data": data})
	return err
}

func (s *saStore) EnsureToolbox(ns, name, image string) error {
	path := "/api/v1/namespaces/" + ns + "/pods/" + name
	status, raw, err := s.do("GET", path, nil)
	if err != nil {
		return err
	}
	if status == 404 {
		pod := toolboxPod(ns, name, image)
		if _, _, err := s.do("POST", "/api/v1/namespaces/"+ns+"/pods", pod); err != nil {
			return err
		}
	}
	return s.waitPodReady(path, raw, 120*time.Second)
}

// waitPodReady polls until the pod's Ready condition is true. raw is the
// GET body from the initial read (may be nil right after a create).
func (s *saStore) waitPodReady(path string, _ []byte, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		_, raw, err := s.do("GET", path, nil)
		if err != nil {
			return err
		}
		var pod struct {
			Status struct {
				Conditions []struct {
					Type   string `json:"type"`
					Status string `json:"status"`
				} `json:"conditions"`
			} `json:"status"`
		}
		if err := json.Unmarshal(raw, &pod); err != nil {
			return fmt.Errorf("decoding pod: %w", err)
		}
		for _, c := range pod.Status.Conditions {
			if c.Type == "Ready" && c.Status == "True" {
				return nil
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("pod %s never became Ready within %s", path, timeout)
		}
		time.Sleep(3 * time.Second)
	}
}

func (s *saStore) ReadSecretKey(ns, name, key string) (string, error) {
	_, raw, err := s.do("GET", "/api/v1/namespaces/"+ns+"/secrets/"+name, nil)
	if err != nil {
		return "", err
	}
	var sec struct {
		Data map[string]string `json:"data"`
	}
	if err := json.Unmarshal(raw, &sec); err != nil {
		return "", err
	}
	return decodeB64(sec.Data[key])
}

// --- kubectl store (local attach) --------------------------------------------

type kubectlStore struct{}

func (kubectlStore) WriteConfigMap(ns, name string, data map[string]string) error {
	args := []string{"-n", ns, "create", "configmap", name}
	for k, v := range data {
		args = append(args, "--from-literal="+k+"="+v)
	}
	args = append(args, "--dry-run=client", "-o", "yaml")
	create := exec.Command("kubectl", args...)
	apply := exec.Command("kubectl", "apply", "-f", "-")
	pipe, err := create.StdoutPipe()
	if err != nil {
		return err
	}
	apply.Stdin = pipe
	apply.Stdout = os.Stderr // progress goes to the log stream
	if err := create.Start(); err != nil {
		return err
	}
	if err := apply.Run(); err != nil {
		_ = create.Wait()
		return fmt.Errorf("kubectl apply configmap: %w", err)
	}
	return create.Wait()
}

func (kubectlStore) EnsureToolbox(ns, name, image string) error {
	if err := exec.Command("kubectl", "-n", ns, "get", "pod", name).Run(); err == nil {
		return nil // exists — the seed never recreates a healthy toolbox
	}
	overrides := fmt.Sprintf(`{"spec":{"securityContext":{"runAsUser":1000,"runAsNonRoot":true,"seccompProfile":{"type":"RuntimeDefault"}}}}`)
	if out, err := exec.Command("kubectl", "-n", ns, "run", name,
		"--image="+image, "--restart=Never", "--overrides="+overrides,
		"--command", "--", "sleep", "infinity").CombinedOutput(); err != nil {
		return fmt.Errorf("kubectl run toolbox: %w (%s)", err, truncate(string(out), 300))
	}
	out, err := exec.Command("kubectl", "-n", ns, "wait", "--for=condition=ready", "--timeout=120s", "pod/"+name).CombinedOutput()
	if err != nil {
		return fmt.Errorf("toolbox never Ready: %w (%s)", err, truncate(string(out), 300))
	}
	return nil
}

func (kubectlStore) ReadSecretKey(ns, name, key string) (string, error) {
	out, err := exec.Command("kubectl", "-n", ns, "get", "secret", name,
		"-o", "jsonpath={.data."+key+"}").Output()
	if err != nil {
		return "", fmt.Errorf("kubectl get secret %s: %w", name, err)
	}
	return decodeB64(strings.TrimSpace(string(out)))
}

// decodeB64 decodes a base64 secret value with a useful error.
func decodeB64(v string) (string, error) {
	if v == "" {
		return "", fmt.Errorf("secret key absent or empty")
	}
	raw, err := base64.StdEncoding.DecodeString(v)
	if err != nil {
		return "", fmt.Errorf("decoding secret value: %w", err)
	}
	return string(raw), nil
}

// --- shared ------------------------------------------------------------------

// toolboxPod is the Ginkgo suite's curl host on the live env (the kind-era
// golden-path-tools equivalent). sleep infinity, NOT the kind-era 3600:
// suite legs may start long after the seed ran (semaphore queueing).
func toolboxPod(ns, name, image string) map[string]any {
	return map[string]any{
		"apiVersion": "v1",
		"kind":       "Pod",
		"metadata": map[string]any{
			"name":      name,
			"namespace": ns,
			"labels": map[string]string{
				"app.kubernetes.io/name":    "e2e-toolbox",
				"app.kubernetes.io/part-of": "inari-e2e",
			},
		},
		"spec": map[string]any{
			"securityContext": map[string]any{
				"runAsUser":      1000,
				"runAsNonRoot":   true,
				"seccompProfile": map[string]string{"type": "RuntimeDefault"},
			},
			"containers": []map[string]any{{
				"name":    "toolbox",
				"image":   image,
				"command": []string{"sleep", "infinity"},
				"resources": map[string]any{
					"requests": map[string]string{"cpu": "10m", "memory": "32Mi"},
					"limits":   map[string]string{"memory": "128Mi"},
				},
			}},
		},
	}
}
