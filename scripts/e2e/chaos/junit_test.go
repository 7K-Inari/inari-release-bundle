package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNativeScoreCannotMaskOtherFailure(t *testing.T) {
	report := &Report{Scenario: "keycloak-latency", RunID: "run-1"}
	report.Add("native phase/verdict", nil, "Completed/Pass")
	report.Add("resilience score = 100%", nil, "native score")
	report.Add("during-fault Ginkgo", errors.New("smoke failed"), "Ginkgo red")
	report.Add("during-fault Playwright", errors.New("smoke failed"), "browser red")
	path := filepath.Join(t.TempDir(), "junit.xml")
	if err := report.WriteJUnit(path); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	xml := string(raw)
	for _, want := range []string{`name="chaos.keycloak-latency"`, `failures="2"`, `resilience score = 100%`, `time="0.000"`, "Ginkgo red", "smoke failed"} {
		if !strings.Contains(xml, want) {
			t.Fatalf("JUnit missing %q:\n%s", want, xml)
		}
	}
	if strings.Contains(xml, "<skipped") || strings.Contains(xml, `time=""`) {
		t.Fatalf("invalid synthetic JUnit:\n%s", xml)
	}
}

func TestEmptyReportStillFailsLoud(t *testing.T) {
	report := &Report{Scenario: "configuration"}
	path := filepath.Join(t.TempDir(), "junit.xml")
	if err := report.WriteJUnit(path); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	if !strings.Contains(string(raw), `failures="1"`) || !strings.Contains(string(raw), "driver produced no checks") {
		t.Fatalf("unexpected JUnit: %s", raw)
	}
}
