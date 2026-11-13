package main

import (
	"context"
	"strings"
	"testing"
)

func TestPrepareIncludesDevDependencies(t *testing.T) {
	runner := &fakeRunner{}
	playwright := &Playwright{Runner: runner, UIDir: t.TempDir(), EvidenceDir: t.TempDir()}
	if err := playwright.Prepare(context.Background()); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(runner.commands[0].args, " ")
	if !strings.Contains(joined, "ci --include=dev") {
		t.Fatalf("npm setup can omit Playwright: %s", joined)
	}
}

func TestNegativeControlDisablesDuringRetries(t *testing.T) {
	runner := &fakeRunner{}
	playwright := &Playwright{Runner: runner, UIDir: t.TempDir(), EvidenceDir: t.TempDir()}
	scenario := scenarioRegistry["negative-control"]
	playwright.Run(context.Background(), scenario, "negative-control", "/tmp/marker")
	if len(runner.commands) != 1 {
		t.Fatalf("commands=%d", len(runner.commands))
	}
	joined := strings.Join(runner.commands[0].args, " ")
	if !strings.Contains(joined, "--retries=0") || !strings.Contains(joined, "@p0|@smoke") {
		t.Fatalf("negative-control args=%s", joined)
	}
	playwright.Run(context.Background(), scenario, "recovery", "")
	recovery := strings.Join(runner.commands[1].args, " ")
	if strings.Contains(recovery, "--retries=0") || !strings.Contains(recovery, "@smoke") {
		t.Fatalf("recovery args=%s", recovery)
	}
}

func TestWaitMarkerEarlyExitCanBeCollectedOnce(t *testing.T) {
	done := make(chan SteadyResult, 1)
	done <- SteadyResult{Err: context.Canceled, Output: "early exit"}
	close(done)
	handle := &SteadyHandle{Suite: "playwright", Marker: t.TempDir() + "/missing-marker", Done: done, Cancel: func() {}}
	if err := handle.WaitMarker(context.Background()); err == nil {
		t.Fatal("early Playwright exit produced no synchronization error")
	}
	for i := 0; i < 2; i++ {
		result := handle.Wait()
		if result.Output != "early exit" {
			t.Fatalf("cached result=%+v", result)
		}
	}
}

func TestPositiveScenarioKeepsCIRetryContract(t *testing.T) {
	runner := &fakeRunner{}
	playwright := &Playwright{Runner: runner, UIDir: t.TempDir(), EvidenceDir: t.TempDir()}
	playwright.Run(context.Background(), scenarioRegistry["keycloak-latency"], "keycloak-latency", "/tmp/marker")
	joined := strings.Join(runner.commands[0].args, " ")
	if strings.Contains(joined, "--retries=0") {
		t.Fatalf("positive scenario changed CI retry behavior: %s", joined)
	}
}
