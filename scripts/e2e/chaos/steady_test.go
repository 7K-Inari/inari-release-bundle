package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGinkgoScopesHAOffAndUsesGateFilter(t *testing.T) {
	runner := &fakeRunner{}
	ginkgo := &Ginkgo{
		Runner:      runner,
		Binary:      "/tools/stack.test",
		EvidenceDir: t.TempDir(),
		Env:         []string{"E2E_HA=true", "E2E_MIGRATIONS_DIR=/should/be/cleared"},
	}
	ginkgo.Run(context.Background(), scenarioRegistry["keycloak-latency"], "keycloak-latency", "/tmp/ginkgo-marker")
	if len(runner.commands) != 1 {
		t.Fatalf("commands=%d", len(runner.commands))
	}
	command := runner.commands[0]
	joined := strings.Join(command.args, " ")
	if strings.Contains(joined, " -args ") {
		t.Fatalf("precompiled test binary must receive Ginkgo flags directly: %s", joined)
	}
	for _, want := range []string{"-test.timeout=15m", "--ginkgo.label-filter=(p0||smoke)&&!quarantine", "--ginkgo.junit-report="} {
		if !strings.Contains(joined, want) {
			t.Fatalf("Ginkgo args missing %q: %s", want, joined)
		}
	}
	var haValues []string
	for _, item := range command.env {
		if strings.HasPrefix(item, "E2E_HA=") {
			haValues = append(haValues, item)
		}
		if item == "E2E_MIGRATIONS_DIR=/should/be/cleared" || item == "E2E_HA=true" {
			t.Fatalf("parent HA environment leaked into Ginkgo: %s", item)
		}
	}
	if len(haValues) != 1 || haValues[0] != "E2E_HA=0" {
		t.Fatalf("E2E_HA override=%v", haValues)
	}
}

func TestGinkgoSmokeAndNegativeSelections(t *testing.T) {
	runner := &fakeRunner{}
	ginkgo := &Ginkgo{Runner: runner, Binary: "/tools/stack.test", EvidenceDir: t.TempDir()}
	ginkgo.Run(context.Background(), scenarioRegistry["nats-outage"], "baseline", "")
	ginkgo.Run(context.Background(), scenarioRegistry["negative-control"], "negative-control", "/tmp/marker")
	baseline := strings.Join(runner.commands[0].args, " ")
	negative := strings.Join(runner.commands[1].args, " ")
	if !strings.Contains(baseline, "--ginkgo.label-filter=smoke") || strings.Contains(baseline, "p0||") {
		t.Fatalf("baseline filter=%s", baseline)
	}
	if !strings.Contains(negative, "--ginkgo.flake-attempts=1") {
		t.Fatalf("negative control can retry past its outage: %s", negative)
	}
}

func TestNegativeControlRequiresEveryIndependentSignal(t *testing.T) {
	all := []bool{true, true, true, true}
	if !negativeControlEffective(all[0], all[1], all[2], all[3]) {
		t.Fatal("all four outage observations should be effective")
	}
	for i := 0; i < 4; i++ {
		signals := append([]bool(nil), all...)
		signals[i] = false
		if negativeControlEffective(signals[0], signals[1], signals[2], signals[3]) {
			t.Fatalf("signal %d was allowed to masquerade as a complete negative control", i)
		}
	}
}

func TestWaitSteadyMarkersRequiresBothSuites(t *testing.T) {
	dir := t.TempDir()
	playwrightMarker := filepath.Join(dir, "playwright.json")
	if err := os.WriteFile(playwrightMarker, []byte(`{"test":"ready"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	done := make(chan SteadyResult, 1)
	done <- SteadyResult{Suite: "ginkgo", Err: errors.New("setup failed"), Output: "early"}
	close(done)
	playwrightDone := make(chan SteadyResult, 1)
	playwrightDone <- SteadyResult{Suite: "playwright", Output: "browser done"}
	close(playwrightDone)
	handles := map[string]*SteadyHandle{
		"ginkgo":     {Suite: "ginkgo", Marker: filepath.Join(dir, "missing-ginkgo.json"), Done: done, Cancel: func() {}},
		"playwright": {Suite: "playwright", Marker: playwrightMarker, Done: playwrightDone, Cancel: func() {}},
	}
	if err := WaitSteadyMarkers(context.Background(), handles); err == nil {
		t.Fatal("one suite marker allowed injection without the other")
	}
	results := CollectSteady(handles, false)
	if results["ginkgo"].Output != "early" {
		t.Fatalf("early Ginkgo result lost: %+v", results["ginkgo"])
	}
}

func TestWaitMarkerRejectsSuiteThatAlreadyCompleted(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "ginkgo.json")
	if err := os.WriteFile(marker, []byte(`{"ready":true}`), 0o644); err != nil {
		t.Fatal(err)
	}
	done := make(chan SteadyResult, 1)
	done <- SteadyResult{Suite: "ginkgo", Output: "finished before Playwright"}
	close(done)
	handle := &SteadyHandle{Suite: "ginkgo", Marker: marker, Done: done, Cancel: func() {}}
	if err := handle.WaitMarker(context.Background()); err == nil {
		t.Fatal("completed suite was accepted as during-fault overlap")
	}
	if result := handle.Wait(); result.Output != "finished before Playwright" {
		t.Fatalf("completed result lost: %+v", result)
	}
}

func TestWaitSteadyMarkersAcceptsBoth(t *testing.T) {
	dir := t.TempDir()
	handles := map[string]*SteadyHandle{}
	for _, suite := range []string{"ginkgo", "playwright"} {
		marker := filepath.Join(dir, suite+".json")
		if err := os.WriteFile(marker, []byte(`{"ready":true}`), 0o644); err != nil {
			t.Fatal(err)
		}
		handles[suite] = &SteadyHandle{Suite: suite, Marker: marker, Done: make(chan SteadyResult), Cancel: func() {}}
	}
	if err := WaitSteadyMarkers(context.Background(), handles); err != nil {
		t.Fatal(err)
	}
}
