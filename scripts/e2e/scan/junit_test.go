package main

import (
	"bytes"
	"encoding/xml"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func readSuite(t *testing.T, path string) junitTestsuite {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read junit: %v", err)
	}
	if !bytes.HasPrefix(body, []byte(xml.Header)) {
		t.Fatal("junit missing the XML declaration at byte 0")
	}
	var suite junitTestsuite
	if err := xml.Unmarshal(body, &suite); err != nil {
		t.Fatalf("junit does not parse: %v", err)
	}
	return suite
}

func TestWriteJUnitPass(t *testing.T) {
	path := filepath.Join(t.TempDir(), "junit.xml")
	results := []ImageResult{
		{Name: "inari-server", Repo: "7k-inari/inari-server", Tag: "4.0.0", Critical: 0, High: 2, Scanned: true},
		{Name: "inari-agent", Repo: "7k-inari/inari-agent", Tag: "v0.6.0", Critical: 0, Scanned: true},
	}
	if err := WriteJUnit(path, results, 0, "run-123"); err != nil {
		t.Fatalf("WriteJUnit: %v", err)
	}
	suite := readSuite(t, path)
	if suite.Name != "security" {
		t.Fatalf("testsuite name = %q, want security (the taxonomy surface label)", suite.Name)
	}
	if suite.Tests != 2 || suite.Failures != 0 {
		t.Fatalf("tests=%d failures=%d, want 2/0", suite.Tests, suite.Failures)
	}
	if suite.Cases[0].Classname != "security.harbor-scan" ||
		suite.Cases[0].Name != "image-scan 7k-inari/inari-server:4.0.0" {
		t.Fatalf("unexpected testcase: %+v", suite.Cases[0])
	}
}

func TestWriteJUnitBreachAndError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "junit.xml")
	results := []ImageResult{
		{Name: "inari-server", Repo: "7k-inari/inari-server", Tag: "4.0.0", Critical: 3, High: 9, Scanned: true,
			Platforms: []PlatformResult{{Platform: "linux/amd64", Digest: "sha256:abc", Critical: 3, High: 9, Scanned: true}}},
		{Name: "inari-agent", Repo: "7k-inari/inari-agent", Tag: "v0.6.0", Err: errTest},
		{Name: "scan-gate-configuration", Err: errTest}, // config-error shape
	}
	if err := WriteJUnit(path, results, 0, "run-123"); err != nil {
		t.Fatalf("WriteJUnit: %v", err)
	}
	suite := readSuite(t, path)
	if suite.Tests != 3 || suite.Failures != 3 {
		t.Fatalf("tests=%d failures=%d, want 3/3", suite.Tests, suite.Failures)
	}
	breach := suite.Cases[0].Failure
	if breach == nil || breach.Message != "3 critical CVE(s) across 1 platform manifest(s) exceed the threshold 0" {
		t.Fatalf("unexpected breach failure: %+v", breach)
	}
	if !strings.Contains(breach.Body, "e2e/run-123/scan/reports/") ||
		!strings.Contains(breach.Body, "linux/amd64: 3 critical, 9 high") {
		t.Fatalf("failure body missing report pointer or per-platform counts: %q", breach.Body)
	}
	if suite.Cases[1].Failure == nil || !strings.Contains(suite.Cases[1].Failure.Message, "scan gate error") {
		t.Fatalf("infra error not mapped to a failure: %+v", suite.Cases[1].Failure)
	}
	if suite.Cases[2].Name != "scan-gate-configuration" {
		t.Fatalf("config testcase name fell through: %q", suite.Cases[2].Name)
	}
}

func TestWriteJUnitUnscannable(t *testing.T) {
	// Coordinator ruling (PR #165): the unscannable console bundle passes
	// loudly — the reason rides in the testcase name, no failure element.
	path := filepath.Join(t.TempDir(), "junit.xml")
	results := []ImageResult{{
		Name: "inari-ui-bundle", Repo: "7k-inari/inari-ui-bundle", Tag: "0.4.0",
		Platforms: []PlatformResult{{Digest: "sha256:abc", Unscannable: true}},
	}}
	if err := WriteJUnit(path, results, 0, "run-123"); err != nil {
		t.Fatalf("WriteJUnit: %v", err)
	}
	suite := readSuite(t, path)
	if suite.Tests != 1 || suite.Failures != 0 {
		t.Fatalf("unscannable must be a passing testcase: tests=%d failures=%d", suite.Tests, suite.Failures)
	}
	tc := suite.Cases[0]
	want := "image-scan 7k-inari/inari-ui-bundle:0.4.0 — unscannable: ORAS bundle artifact — JS deps owned by inari-ui CI"
	if tc.Name != want {
		t.Fatalf("testcase name:\n got %q\nwant %q", tc.Name, want)
	}
	if tc.Failure != nil {
		t.Fatalf("unscannable must not carry a failure: %+v", tc.Failure)
	}
}

func TestPlatformErrorFails(t *testing.T) {
	r := ImageResult{
		Platforms: []PlatformResult{
			{Platform: "linux/amd64", Scanned: true},
			{Platform: "linux/arm64", Err: errTest},
		},
	}
	if !r.Breached(100) {
		t.Fatal("a platform error must fail the image regardless of the threshold")
	}
	f := r.failure(0, "")
	if !strings.Contains(f.Message, "linux/arm64") {
		t.Fatalf("platform error not named: %q", f.Message)
	}
}

func TestThresholdSemantics(t *testing.T) {
	r := ImageResult{Critical: 2}
	if r.Breached(2) {
		t.Fatal("equal to the threshold must pass (fail when exceeding it)")
	}
	if !r.Breached(1) {
		t.Fatal("above the threshold must fail")
	}
	// -1 force-fails every image (Critical ≥ 0 > -1) — the live self-check
	// of the >threshold verdict path.
	if !(&ImageResult{Critical: 0}).Breached(-1) {
		t.Fatal("threshold -1 must fail even a zero-critical image")
	}
}

var errTest = os.ErrNotExist

// run() configuration-error paths: no HTTP needed, so these exercise main's
// wiring end to end.
func TestRunMissingCredentials(t *testing.T) {
	dir := t.TempDir()
	env := map[string]string{
		"OUT_DIR": dir, "REPORTS_DIR": filepath.Join(dir, "reports"),
		"SERVER_IMAGE": "4.0.0", "CONSOLE_IMAGE": "0.4.0", "AGENT_IMAGE": "0.6.0",
		"SCAN_RUN_ID": "run-x",
	}
	var out bytes.Buffer
	rc := run(func(k string) string { return env[k] }, &out)
	if rc != 1 {
		t.Fatalf("rc = %d, want 1", rc)
	}
	if !strings.Contains(out.String(), "harbor-scanner") {
		t.Fatalf("output does not name the missing secret: %q", out.String())
	}
	suite := readSuite(t, filepath.Join(dir, "junit.xml"))
	if suite.Failures != 1 || suite.Cases[0].Name != "scan-gate-configuration" {
		t.Fatalf("config failure not reported through junit: %+v", suite)
	}
}

func TestRunBadThreshold(t *testing.T) {
	dir := t.TempDir()
	env := map[string]string{
		"OUT_DIR": dir, "REPORTS_DIR": filepath.Join(dir, "reports"),
		"SERVER_IMAGE": "4.0.0", "CONSOLE_IMAGE": "0.4.0", "AGENT_IMAGE": "0.6.0",
		"HARBOR_ROBOT_USERNAME": "u", "HARBOR_ROBOT_TOKEN": "p",
		"SCAN_CRITICAL_THRESHOLD": "soon",
	}
	var out bytes.Buffer
	if rc := run(func(k string) string { return env[k] }, &out); rc != 1 {
		t.Fatalf("rc = %d, want 1", rc)
	}
	if !strings.Contains(out.String(), "SCAN_CRITICAL_THRESHOLD") {
		t.Fatalf("output does not name the bad parameter: %q", out.String())
	}
}

func TestRunMissingImageTag(t *testing.T) {
	dir := t.TempDir()
	env := map[string]string{
		"OUT_DIR": dir, "REPORTS_DIR": filepath.Join(dir, "reports"),
		"HARBOR_ROBOT_USERNAME": "u", "HARBOR_ROBOT_TOKEN": "p",
		"SERVER_IMAGE": "4.0.0", // console + agent empty
	}
	var out bytes.Buffer
	if rc := run(func(k string) string { return env[k] }, &out); rc != 1 {
		t.Fatalf("rc = %d, want 1", rc)
	}
	if !strings.Contains(out.String(), "inari-ui-bundle") || !strings.Contains(out.String(), "inari-agent") {
		t.Fatalf("output does not name the empty tags: %q", out.String())
	}
}
