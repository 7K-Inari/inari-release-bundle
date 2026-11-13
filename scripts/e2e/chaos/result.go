package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// ChaosResult models the native fields without depending on a moving Go CRD
// module. Unknown fields remain available in the archived raw JSON.
type ChaosResult struct {
	Metadata struct {
		Name        string            `json:"name"`
		Namespace   string            `json:"namespace"`
		Labels      map[string]string `json:"labels"`
		Annotations map[string]string `json:"annotations"`
	} `json:"metadata"`
	Status struct {
		ExperimentStatus struct {
			Phase                  string          `json:"phase"`
			Verdict                string          `json:"verdict"`
			ProbeSuccessPercentage json.RawMessage `json:"probeSuccessPercentage"`
			FailStep               string          `json:"failStep"`
		} `json:"experimentStatus"`
	} `json:"status"`
}

func parseChaosResult(raw []byte, wantName string) (ChaosResult, error) {
	var result ChaosResult
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := decoder.Decode(&result); err != nil {
		return result, fmt.Errorf("decode ChaosResult: %w", err)
	}
	if result.Metadata.Name != wantName {
		return result, fmt.Errorf("ChaosResult name=%q, want %q", result.Metadata.Name, wantName)
	}
	if result.Metadata.Namespace != e2eNamespace {
		return result, fmt.Errorf("ChaosResult namespace=%q, want %q", result.Metadata.Namespace, e2eNamespace)
	}
	return result, nil
}

func (r ChaosResult) Phase() string   { return r.Status.ExperimentStatus.Phase }
func (r ChaosResult) Verdict() string { return r.Status.ExperimentStatus.Verdict }

func (r ChaosResult) Terminal() bool {
	switch r.Phase() {
	case "Completed", "Completed_With_Probe_Failure", "Stopped", "Error":
		return true
	default:
		return false
	}
}

func (r ChaosResult) Running() bool {
	return r.Phase() == "Running"
}

// duringReady prevents Litmus's start-of-test Running phase from releasing
// network assertions before every snapshotted target is actually injected.
func duringReady(result ChaosResult, engine *Engine) bool {
	if !result.Running() {
		return false
	}
	switch engine.Definition.Fault {
	case "pod-network-latency", "pod-network-loss":
		if len(engine.TargetPods) == 0 {
			return false
		}
		for _, pod := range engine.TargetPods {
			if result.Metadata.Annotations["pod/"+pod] != "injected" {
				return false
			}
		}
	}
	return true
}

func (r ChaosResult) Score() (string, error) {
	raw := r.Status.ExperimentStatus.ProbeSuccessPercentage
	if len(raw) == 0 || string(raw) == "null" {
		return "", fmt.Errorf("probeSuccessPercentage is missing")
	}
	var text string
	if raw[0] == '"' {
		if err := json.Unmarshal(raw, &text); err != nil {
			return "", fmt.Errorf("probeSuccessPercentage: %w", err)
		}
	} else {
		text = string(raw)
	}
	text = strings.TrimSuffix(strings.TrimSpace(text), "%")
	value, err := strconv.ParseFloat(text, 64)
	if err != nil || value < 0 || value > 100 {
		return "", fmt.Errorf("probeSuccessPercentage must be numeric in [0,100], got %q", text)
	}
	return text, nil
}

func (r ChaosResult) Passed() error {
	if r.Phase() != "Completed" {
		return fmt.Errorf("native phase=%q, want Completed (failStep=%q)", r.Phase(), r.Status.ExperimentStatus.FailStep)
	}
	if r.Verdict() != "Pass" {
		return fmt.Errorf("native verdict=%q, want Pass (failStep=%q)", r.Verdict(), r.Status.ExperimentStatus.FailStep)
	}
	return nil
}

func (r ChaosResult) FailedAsExpected() bool {
	return r.Terminal() && (r.Verdict() == "Fail" || r.Phase() == "Completed_With_Probe_Failure" || r.Phase() == "Error")
}
