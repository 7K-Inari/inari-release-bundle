package main

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"text/template"
	"time"
)

//go:embed manifests
var manifestFS embed.FS

// CommandRunner is the small exec seam used by kubectl and Playwright fakes.
type CommandRunner interface {
	Run(ctx context.Context, dir string, env []string, input []byte, name string, args ...string) (string, error)
}

type ExecRunner struct{ Stdout io.Writer }

func (r ExecRunner) Run(ctx context.Context, dir string, env []string, input []byte, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	if input != nil {
		cmd.Stdin = bytes.NewReader(input)
	}
	output, err := cmd.CombinedOutput()
	if r.Stdout != nil && len(output) > 0 {
		fmt.Fprintln(r.Stdout, strings.TrimSpace(string(output)))
	}
	if err != nil {
		return string(output), fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	return string(output), nil
}

type KubeClient struct {
	Binary    string
	Namespace string
	Runner    CommandRunner
}

func (k *KubeClient) Output(ctx context.Context, input []byte, args ...string) (string, error) {
	if k.Namespace != e2eNamespace {
		return "", fmt.Errorf("kubectl namespace guard: %q", k.Namespace)
	}
	binary := k.Binary
	if binary == "" {
		binary = "kubectl"
	}
	all := []string{"-n", k.Namespace}
	all = append(all, args...)
	return k.Runner.Run(ctx, "", nil, input, binary, all...)
}

func (k *KubeClient) ClusterOutput(ctx context.Context, args ...string) (string, error) {
	binary := k.Binary
	if binary == "" {
		binary = "kubectl"
	}
	return k.Runner.Run(ctx, "", nil, nil, binary, args...)
}

func (k *KubeClient) Apply(ctx context.Context, manifest []byte) error {
	_, err := k.Output(ctx, manifest, "apply", "-f", "-")
	return err
}

func (k *KubeClient) GetJSON(ctx context.Context, resource, name string) ([]byte, error) {
	out, err := k.Output(ctx, nil, "get", resource, name, "-o", "json")
	return []byte(out), err
}

func (k *KubeClient) Delete(ctx context.Context, resource, name string) error {
	_, err := k.Output(ctx, nil, "delete", resource, name, "--ignore-not-found", "--wait=true", "--timeout=45s")
	return err
}

func (k *KubeClient) NamesByLabel(ctx context.Context, resource, selector string) ([]string, error) {
	out, err := k.Output(ctx, nil, "get", resource, "-l", selector, "-o", "jsonpath={.items[*].metadata.name}")
	if err != nil {
		return nil, err
	}
	return strings.Fields(out), nil
}

type engineDefinition struct {
	Key              string
	Fault            string
	DurationSeconds  int
	TargetContainer  string
	DestinationHosts string
	LatencyMS        int
	LossPercent      int
	Playwright       bool
}

var engineRegistry = map[string]engineDefinition{
	"cnpg-primary-delete": {Key: "cnpg-primary-delete", Fault: "pod-delete", DurationSeconds: 120, TargetContainer: "postgres", Playwright: true},
	"keycloak-latency":    {Key: "keycloak-latency", Fault: "pod-network-latency", DurationSeconds: 120, TargetContainer: "server", DestinationHosts: "keycloak-service.inari-e2e.svc.cluster.local", LatencyMS: 250, Playwright: true},
	"nats-outage":         {Key: "nats-outage", Fault: "pod-network-loss", DurationSeconds: 120, TargetContainer: "nats", LossPercent: 100, Playwright: true},
	"openfga-latency":     {Key: "openfga-latency", Fault: "pod-network-latency", DurationSeconds: 120, TargetContainer: "server", DestinationHosts: "openfga.inari-e2e.svc.cluster.local", LatencyMS: 250, Playwright: true},
	"openfga-timeout":     {Key: "openfga-timeout", Fault: "pod-network-latency", DurationSeconds: 120, TargetContainer: "server", DestinationHosts: "openfga.inari-e2e.svc.cluster.local", LatencyMS: 90000},
	"server-pod-delete":   {Key: "server-pod-delete", Fault: "pod-delete", DurationSeconds: 120, TargetContainer: "server", Playwright: true},
	"negative-control":    {Key: "negative-control", Fault: "pod-network-loss", DurationSeconds: 120, TargetContainer: "server", LossPercent: 100, Playwright: true},
}

type Engine struct {
	Definition        engineDefinition
	Name              string
	ExperimentName    string
	ResultName        string
	TargetPods        []string
	Manifest          []byte
	Experiment        []byte
	ExperimentApplied bool
	Applied           bool
	RawResult         []byte
}

type templateData struct {
	Name             string
	ExperimentName   string
	RunID            string
	TargetPods       string
	TargetContainer  string
	DestinationHosts string
	DurationSeconds  int
	LatencyMS        int
	LossPercent      int
	APIURL           string
	KeycloakURL      string
	OpenFGAHealthURL string
}

func renderEngine(cfg Config, key string, targets []string) (*Engine, error) {
	def, ok := engineRegistry[key]
	if !ok {
		return nil, fmt.Errorf("unknown engine %q", key)
	}
	if len(targets) == 0 {
		return nil, fmt.Errorf("engine %s has an empty target snapshot", key)
	}
	short := shortRunID(cfg.RunID)
	engine := &Engine{
		Definition:     def,
		Name:           "e2e-" + short + "-" + key,
		ExperimentName: def.Fault + "-" + short,
		TargetPods:     append([]string(nil), targets...),
	}
	engine.ResultName = engine.Name + "-" + engine.ExperimentName
	if len(engine.ResultName) > 63 {
		return nil, fmt.Errorf("generated ChaosResult name exceeds 63 characters: %s", engine.ResultName)
	}
	data := templateData{
		Name:             engine.Name,
		ExperimentName:   engine.ExperimentName,
		RunID:            cfg.RunID,
		TargetPods:       strings.Join(targets, ","),
		TargetContainer:  def.TargetContainer,
		DestinationHosts: def.DestinationHosts,
		DurationSeconds:  def.DurationSeconds,
		LatencyMS:        def.LatencyMS,
		LossPercent:      def.LossPercent,
		APIURL:           "https://" + cfg.BaseDomain + "/api/v1/tenants",
		KeycloakURL:      cfg.KCURL + "/realms/inari/.well-known/openid-configuration",
		OpenFGAHealthURL: "http://openfga.inari-e2e.svc.cluster.local:8080/healthz",
	}
	functions := template.FuncMap{"join": strings.Join}
	for _, item := range []struct {
		path   string
		output *[]byte
	}{
		{path: "manifests/engines/" + key + ".yaml", output: &engine.Manifest},
		{path: "manifests/experiments/" + def.Fault + ".yaml", output: &engine.Experiment},
	} {
		raw, err := manifestFS.ReadFile(item.path)
		if err != nil {
			return nil, err
		}
		tmpl, err := template.New(filepath.Base(item.path)).Funcs(functions).Option("missingkey=error").Parse(string(raw))
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", item.path, err)
		}
		var rendered bytes.Buffer
		if err := tmpl.Execute(&rendered, data); err != nil {
			return nil, fmt.Errorf("render %s: %w", item.path, err)
		}
		*item.output = rendered.Bytes()
	}
	for _, manifest := range [][]byte{engine.Manifest, engine.Experiment} {
		if err := validateManifestNamespace(manifest); err != nil {
			return nil, fmt.Errorf("%s: %w", key, err)
		}
	}
	for _, pod := range targets {
		if !bytes.Contains(engine.Manifest, []byte(pod)) {
			return nil, fmt.Errorf("engine %s omitted snapshotted target %q", key, pod)
		}
	}
	return engine, nil
}

var namespaceLine = regexp.MustCompile(`(?m)^\s*(?:namespace|appns):\s*["']?([^\s#"']+)`)

func validateManifestNamespace(manifest []byte) error {
	matches := namespaceLine.FindAllSubmatch(manifest, -1)
	if len(matches) == 0 {
		return fmt.Errorf("rendered manifest has no explicit namespace")
	}
	for _, match := range matches {
		if string(match[1]) != e2eNamespace {
			return fmt.Errorf("forbidden namespace %q in rendered manifest", match[1])
		}
	}
	for _, forbidden := range []string{"kind: ClusterRole", "kind: ClusterRoleBinding", "inari-system", "inari.7kgroup.org", "auth.7kgroup.org"} {
		if bytes.Contains(manifest, []byte(forbidden)) {
			return fmt.Errorf("forbidden live/cluster-wide value %q in rendered manifest", forbidden)
		}
	}
	return nil
}

func (e *Engine) WriteManifests(evidenceDir string) error {
	for _, item := range []struct {
		dir  string
		name string
		raw  []byte
	}{
		{dir: "engines", name: e.Definition.Key + ".yaml", raw: e.Manifest},
		{dir: "experiments", name: e.Definition.Key + ".yaml", raw: e.Experiment},
	} {
		dir := filepath.Join(evidenceDir, item.dir)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dir, item.name), item.raw, 0o644); err != nil {
			return err
		}
	}
	return nil
}

type Pod struct {
	Metadata struct {
		Name      string            `json:"name"`
		Namespace string            `json:"namespace"`
		Labels    map[string]string `json:"labels"`
	} `json:"metadata"`
	Spec struct {
		Containers []struct {
			Name string `json:"name"`
		} `json:"containers"`
	} `json:"spec"`
	Status struct {
		Phase      string `json:"phase"`
		Conditions []struct {
			Type   string `json:"type"`
			Status string `json:"status"`
		} `json:"conditions"`
	} `json:"status"`
}

func (p Pod) Ready() bool {
	if p.Status.Phase != "Running" {
		return false
	}
	for _, condition := range p.Status.Conditions {
		if condition.Type == "Ready" {
			return condition.Status == "True"
		}
	}
	return false
}

func (p Pod) HasContainer(name string) bool {
	for _, container := range p.Spec.Containers {
		if container.Name == name {
			return true
		}
	}
	return false
}

func listPods(ctx context.Context, kube *KubeClient, selector string) ([]Pod, error) {
	out, err := kube.Output(ctx, nil, "get", "pods", "-l", selector, "-o", "json")
	if err != nil {
		return nil, err
	}
	var list struct {
		Items []Pod `json:"items"`
	}
	if err := json.Unmarshal([]byte(out), &list); err != nil {
		return nil, err
	}
	for _, pod := range list.Items {
		if pod.Metadata.Namespace != e2eNamespace {
			return nil, fmt.Errorf("target pod %s escaped into namespace %q", pod.Metadata.Name, pod.Metadata.Namespace)
		}
	}
	return list.Items, nil
}

func readyPodNames(pods []Pod, container string) ([]string, error) {
	var names []string
	for _, pod := range pods {
		if !pod.Ready() {
			continue
		}
		if !pod.HasContainer(container) {
			return nil, fmt.Errorf("target pod %s has no %q container", pod.Metadata.Name, container)
		}
		names = append(names, pod.Metadata.Name)
	}
	sort.Strings(names)
	return names, nil
}

func resolveTargets(ctx context.Context, cfg Config, kube *KubeClient, key string) ([]string, error) {
	def := engineRegistry[key]
	if key == "cnpg-primary-delete" {
		raw, err := kube.GetJSON(ctx, "cluster.postgresql.cnpg.io", "postgresql")
		if err != nil {
			return nil, err
		}
		var cluster struct {
			Status struct {
				CurrentPrimary string `json:"currentPrimary"`
			} `json:"status"`
		}
		if err := json.Unmarshal(raw, &cluster); err != nil {
			return nil, err
		}
		if cluster.Status.CurrentPrimary == "" {
			return nil, fmt.Errorf("CNPG currentPrimary is empty")
		}
		pods, err := listPods(ctx, kube, "cnpg.io/cluster=postgresql")
		if err != nil {
			return nil, err
		}
		for _, pod := range pods {
			if pod.Metadata.Name == cluster.Status.CurrentPrimary {
				if !pod.Ready() || !pod.HasContainer("postgres") {
					return nil, fmt.Errorf("CNPG primary %s is not a Ready postgres pod", pod.Metadata.Name)
				}
				return []string{pod.Metadata.Name}, nil
			}
		}
		return nil, fmt.Errorf("CNPG primary %s is absent from the e2e pod snapshot", cluster.Status.CurrentPrimary)
	}
	if key == "nats-outage" {
		pods, err := listPods(ctx, kube, "app.kubernetes.io/name=nats,app.kubernetes.io/instance=nats,app.kubernetes.io/component=nats")
		if err != nil {
			return nil, err
		}
		names, err := readyPodNames(pods, def.TargetContainer)
		if err != nil {
			return nil, err
		}
		if len(names) != 3 {
			return nil, fmt.Errorf("NATS target snapshot has %d Ready pods, want exactly 3", len(names))
		}
		return names, nil
	}
	pods, err := listPods(ctx, kube, "app.kubernetes.io/name=inari-server,app.kubernetes.io/instance=inari-server,app.kubernetes.io/component=inari-server")
	if err != nil {
		return nil, err
	}
	names, err := readyPodNames(pods, def.TargetContainer)
	if err != nil {
		return nil, err
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("no Ready inari-server targets")
	}
	switch key {
	case "server-pod-delete":
		if len(names) != 2 {
			return nil, fmt.Errorf("server replacement requires exactly 2 Ready replicas, got %d", len(names))
		}
		return names[:1], nil
	case "negative-control":
		if len(names) != 2 {
			return nil, fmt.Errorf("negative control requires exactly 2 Ready replicas, got %d", len(names))
		}
	}
	return names, nil
}

func preflight(ctx context.Context, kube *KubeClient) error {
	out, err := kube.ClusterOutput(ctx, "api-resources", "--api-group=litmuschaos.io", "-o", "name")
	if err != nil {
		return fmt.Errorf("Litmus API discovery: %w", err)
	}
	for _, resource := range []string{"chaosengines", "chaosexperiments", "chaosresults"} {
		found := false
		for _, line := range strings.Fields(out) {
			if line == resource || strings.HasPrefix(line, resource+".") {
				found = true
			}
		}
		if !found {
			return fmt.Errorf("Litmus resource %s is not served", resource)
		}
	}
	raw, err := kube.GetJSON(ctx, "deployment", "litmus")
	if err != nil {
		return fmt.Errorf("Litmus operator: %w", err)
	}
	var deployment struct {
		Status struct {
			ReadyReplicas int `json:"readyReplicas"`
		} `json:"status"`
	}
	if err := json.Unmarshal(raw, &deployment); err != nil {
		return err
	}
	if deployment.Status.ReadyReplicas != 1 {
		return fmt.Errorf("Litmus operator readyReplicas=%d, want 1", deployment.Status.ReadyReplicas)
	}
	return nil
}

func ensureNoStaleResources(ctx context.Context, kube *KubeClient, runID string) error {
	for _, resource := range []string{"chaosengines", "chaosresults"} {
		names, err := kube.NamesByLabel(ctx, resource, "e2e.inari.io/run-id="+runID)
		if err != nil {
			return err
		}
		if len(names) != 0 {
			return fmt.Errorf("stale %s for run %s: %s", resource, runID, strings.Join(names, ","))
		}
	}
	return nil
}

func waitForResult(ctx context.Context, kube *KubeClient, engine *Engine, terminal bool) ([]byte, error) {
	deadline := time.Now().Add(6 * time.Minute)
	var last []byte
	var lastErr error
	for time.Now().Before(deadline) {
		raw, err := kube.GetJSON(ctx, "chaosresult", engine.ResultName)
		if err == nil {
			last = raw
			result, parseErr := parseChaosResult(raw, engine.ResultName)
			if parseErr != nil {
				lastErr = parseErr
			} else if terminal && result.Terminal() {
				return raw, nil
			} else if !terminal && duringReady(result, engine) {
				return raw, nil
			}
		} else {
			lastErr = err
		}
		select {
		case <-ctx.Done():
			return last, ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	if lastErr != nil {
		return last, fmt.Errorf("waiting for ChaosResult %s: %w", engine.ResultName, lastErr)
	}
	return last, fmt.Errorf("waiting for ChaosResult %s: deadline exceeded", engine.ResultName)
}

func collectRecordedResources(raw []byte) (pods, jobs []string) {
	var engine struct {
		Status struct {
			Experiments []struct {
				RunnerPod     string `json:"runnerPod"`
				ExperimentPod string `json:"experimentPod"`
				JobName       string `json:"jobName"`
			} `json:"experiments"`
		} `json:"status"`
	}
	if json.Unmarshal(raw, &engine) != nil {
		return nil, nil
	}
	for _, experiment := range engine.Status.Experiments {
		for _, pod := range []string{experiment.RunnerPod, experiment.ExperimentPod} {
			if pod != "" {
				pods = append(pods, pod)
			}
		}
		if experiment.JobName != "" {
			jobs = append(jobs, experiment.JobName)
		}
	}
	return pods, jobs
}

// CleanupEngine uses only the exact run-scoped object names and resources
// recorded in engine status. It never deletes by a broad application label.
func CleanupEngine(ctx context.Context, kube *KubeClient, engine *Engine) error {
	if !engine.Applied && !engine.ExperimentApplied {
		return nil
	}
	var errs []string
	raw, err := kube.GetJSON(ctx, "chaosengine", engine.Name)
	if err != nil {
		raw = engine.Manifest
	}
	pods, jobs := collectRecordedResources(raw)
	_, _ = kube.Output(ctx, nil, "patch", "chaosengine", engine.Name, "--type=merge", "-p", `{"spec":{"engineState":"stop"}}`)
	for _, item := range []struct{ resource, name string }{{"chaosengine", engine.Name}, {"chaosresult", engine.ResultName}, {"chaosexperiment", engine.ExperimentName}} {
		if err := kube.Delete(ctx, item.resource, item.name); err != nil {
			errs = append(errs, fmt.Sprintf("delete %s/%s: %v", item.resource, item.name, err))
		}
	}
	for _, pod := range pods {
		if err := kube.Delete(ctx, "pod", pod); err != nil {
			errs = append(errs, fmt.Sprintf("delete recorded pod/%s: %v", pod, err))
		}
	}
	for _, job := range jobs {
		if err := kube.Delete(ctx, "job", job); err != nil {
			errs = append(errs, fmt.Sprintf("delete recorded job/%s: %v", job, err))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return nil
}

func leftoverResources(ctx context.Context, kube *KubeClient, engine *Engine, runID string) error {
	var leftovers []string
	for _, resource := range []string{"chaosengines", "chaosresults", "chaosexperiments", "pods", "jobs"} {
		for _, selector := range []string{"e2e.inari.io/run-id=" + runID, "chaosEngineName=" + engine.Name} {
			names, err := kube.NamesByLabel(ctx, resource, selector)
			if err != nil {
				return err
			}
			for _, name := range names {
				leftovers = append(leftovers, resource+"/"+name)
			}
		}
	}
	if len(leftovers) > 0 {
		sort.Strings(leftovers)
		return fmt.Errorf("leftover chaos resources: %s", strings.Join(leftovers, ", "))
	}
	return nil
}
