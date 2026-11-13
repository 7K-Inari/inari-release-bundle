package main

import (
	"fmt"
	"testing"
)

func resultJSON(phase, verdict, score string) []byte {
	return []byte(fmt.Sprintf(`{"metadata":{"name":"engine-result","namespace":"inari-e2e"},"status":{"experimentStatus":{"phase":%q,"verdict":%q,"probeSuccessPercentage":%s}}}`, phase, verdict, score))
}

func TestChaosResultStateMachine(t *testing.T) {
	for _, phase := range []string{"Completed", "Completed_With_Probe_Failure", "Stopped", "Error"} {
		result, err := parseChaosResult(resultJSON(phase, "Pass", `"100"`), "engine-result")
		if err != nil || !result.Terminal() {
			t.Fatalf("phase %s: terminal=%t err=%v", phase, result.Terminal(), err)
		}
	}
	result, err := parseChaosResult(resultJSON("Running", "Awaited", `"0"`), "engine-result")
	if err != nil || !result.Running() || result.Terminal() {
		t.Fatalf("running result=%+v err=%v", result, err)
	}
}

func TestNetworkDuringChecksWaitForEveryTargetInjection(t *testing.T) {
	result, err := parseChaosResult(resultJSON("Running", "Awaited", `"0"`), "engine-result")
	if err != nil {
		t.Fatal(err)
	}
	engine := &Engine{Definition: engineDefinition{Fault: "pod-network-latency"}, TargetPods: []string{"pod-a", "pod-b"}}
	if duringReady(result, engine) {
		t.Fatal("Litmus SOT Running state was mistaken for injection")
	}
	result.Metadata.Annotations = map[string]string{"pod/pod-a": "injected"}
	if duringReady(result, engine) {
		t.Fatal("partial target injection released the during checks")
	}
	result.Metadata.Annotations["pod/pod-b"] = "injected"
	if !duringReady(result, engine) {
		t.Fatal("complete target injection did not release the during checks")
	}
	engine.Definition.Fault = "pod-delete"
	result.Metadata.Annotations = nil
	if !duringReady(result, engine) {
		t.Fatal("pod-delete has no injection annotations and must retain its Running gate")
	}
}

func TestChaosResultValidation(t *testing.T) {
	tests := []struct {
		name string
		raw  []byte
	}{
		{name: "wrong namespace", raw: []byte(`{"metadata":{"name":"engine-result","namespace":"inari-system"},"status":{}}`)},
		{name: "wrong name", raw: []byte(`{"metadata":{"name":"other","namespace":"inari-e2e"},"status":{}}`)},
		{name: "malformed", raw: []byte(`{`)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := parseChaosResult(tt.raw, "engine-result"); err == nil {
				t.Fatal("expected error")
			}
		})
	}
	for _, score := range []string{`""`, `"abc"`, `"-1"`, `"101"`, `null`} {
		result, err := parseChaosResult(resultJSON("Completed", "Pass", score), "engine-result")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := result.Score(); err == nil {
			t.Fatalf("score %s unexpectedly accepted", score)
		}
	}
	for _, score := range []string{`"100"`, `"99.5"`, `0`, `"0%"`} {
		result, err := parseChaosResult(resultJSON("Completed", "Pass", score), "engine-result")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := result.Score(); err != nil {
			t.Fatalf("score %s: %v", score, err)
		}
	}
}

func TestNativePassIsStrict(t *testing.T) {
	for _, tt := range []struct{ phase, verdict string }{{"Completed", "Fail"}, {"Completed_With_Probe_Failure", "Pass"}, {"Stopped", "Pass"}, {"Completed", "Awaited"}} {
		result, err := parseChaosResult(resultJSON(tt.phase, tt.verdict, `"100"`), "engine-result")
		if err != nil {
			t.Fatal(err)
		}
		if result.Passed() == nil {
			t.Fatalf("%s/%s unexpectedly passed", tt.phase, tt.verdict)
		}
	}
}
