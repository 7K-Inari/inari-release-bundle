package main

import (
	"context"
	"strings"
	"testing"
)

func TestRenderEveryEngineIsPinnedAndNamespaced(t *testing.T) {
	env := validEnv()
	cfg, err := loadConfig(getenvFor(env))
	if err != nil {
		t.Fatal(err)
	}
	for key, def := range engineRegistry {
		t.Run(key, func(t *testing.T) {
			engine, err := renderEngine(cfg, key, []string{"pod-a", "pod-b"})
			if err != nil {
				t.Fatal(err)
			}
			for _, manifest := range [][]byte{engine.Manifest, engine.Experiment} {
				text := string(manifest)
				if !strings.Contains(text, "namespace: inari-e2e") {
					t.Fatalf("missing e2e namespace:\n%s", text)
				}
				for _, forbidden := range []string{"inari-system", "kind: ClusterRole", "gaiadocker/iproute2\n", "go-runner:latest"} {
					if strings.Contains(text, forbidden) {
						t.Fatalf("forbidden %q:\n%s", forbidden, text)
					}
				}
			}
			if !strings.Contains(string(engine.Manifest), "e2e-123e4567-"+key) || !strings.Contains(string(engine.Manifest), "pod-a,pod-b") {
				t.Fatalf("run-scoped names/targets missing:\n%s", engine.Manifest)
			}
			if len(engine.ResultName) > 63 {
				t.Fatalf("result name exceeds runner/job-safe length: %s", engine.ResultName)
			}
			if def.Fault == "pod-network-latency" || def.Fault == "pod-network-loss" {
				if !strings.Contains(string(engine.Experiment), "gaiadocker/iproute2@sha256:d0cd5819fbeaf2e7501dcb223bb5bad4fda7c1e76ff9877baba6e559c70b6d1e") {
					t.Fatal("network helper digest is not pinned")
				}
			}
			if !strings.Contains(string(engine.Experiment), "go-runner:3.31.0") {
				t.Fatal("fault image is not pinned to 3.31.0")
			}
		})
	}
}

func TestManifestNamespaceGuardFailsClosed(t *testing.T) {
	for _, value := range []string{"inari-system", "default", "litmus"} {
		manifest := []byte("metadata:\n  namespace: " + value + "\n")
		if err := validateManifestNamespace(manifest); err == nil {
			t.Fatalf("namespace %s accepted", value)
		}
	}
	if err := validateManifestNamespace([]byte("metadata: {}\n")); err == nil {
		t.Fatal("manifest without namespace accepted")
	}
}

func TestOpenFGATimeoutHasBoundedFault(t *testing.T) {
	cfg, err := loadConfig(getenvFor(validEnv()))
	if err != nil {
		t.Fatal(err)
	}
	engine, err := renderEngine(cfg, "openfga-timeout", []string{"server-0"})
	if err != nil {
		t.Fatal(err)
	}
	text := string(engine.Manifest)
	for _, want := range []string{`name: NETWORK_LATENCY, value: "90000"`, `name: TOTAL_CHAOS_DURATION, value: "120"`, "mode: EOT", "openfga.inari-e2e.svc.cluster.local"} {
		if !strings.Contains(text, want) {
			t.Fatalf("timeout engine missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "mode: Continuous") {
		t.Fatal("expected timeout window must not require a continuous native pass")
	}
}

type recordedCommand struct {
	name string
	args []string
	env  []string
}

type fakeRunner struct {
	commands []recordedCommand
	output   string
}

func (f *fakeRunner) Run(_ context.Context, _ string, env []string, _ []byte, name string, args ...string) (string, error) {
	f.commands = append(f.commands, recordedCommand{name: name, args: append([]string(nil), args...), env: append([]string(nil), env...)})
	return f.output, nil
}

func TestCleanupUsesExactNamesOnly(t *testing.T) {
	runner := &fakeRunner{output: `{"status":{"experiments":[{"runnerPod":"runner-1","experimentPod":"experiment-1","jobName":"job-1"}]}}`}
	kube := &KubeClient{Binary: "kubectl", Namespace: e2eNamespace, Runner: runner}
	engine := &Engine{Name: "engine-1", ExperimentName: "experiment-def-1", ResultName: "result-1", Applied: true, ExperimentApplied: true}
	if err := CleanupEngine(context.Background(), kube, engine); err != nil {
		t.Fatal(err)
	}
	var deletes []string
	for _, command := range runner.commands {
		joined := strings.Join(command.args, " ")
		if strings.Contains(joined, "delete") {
			deletes = append(deletes, joined)
			if strings.Contains(joined, " -l ") || strings.Contains(joined, "--all") {
				t.Fatalf("broad deletion: %s", joined)
			}
		}
	}
	for _, name := range []string{"engine-1", "result-1", "experiment-def-1", "runner-1", "experiment-1", "job-1"} {
		found := false
		for _, command := range deletes {
			if strings.Contains(command, name) {
				found = true
			}
		}
		if !found {
			t.Errorf("no exact delete for %s: %v", name, deletes)
		}
	}
}

func TestEmptyTargetsNeverRender(t *testing.T) {
	cfg, err := loadConfig(getenvFor(validEnv()))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := renderEngine(cfg, "keycloak-latency", nil); err == nil {
		t.Fatal("empty target snapshot accepted")
	}
}
