package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// fakeHarbor is a scriptable Harbor stand-in: the artifact overview walks a
// caller-supplied status sequence, and every endpoint hit is recorded.
type fakeHarbor struct {
	t           *testing.T
	statuses    []string // polled in order; the last one repeats
	overviewIdx atomic.Int32
	scanPosts   atomic.Int32
	primePulls  atomic.Int32
	overview404 int32  // first N overview GETs return 404
	reportBody  string // additions/vulnerabilities payload
	reportCode  int
}

const testRepo = "7k-inari/inari-server"
const testTag = "1.2.3"
const testEscRepo = "7k-inari%252Finari-server"

func (f *fakeHarbor) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/service/token", func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Query().Get("scope"), "repository:ghcr-proxy/"+testRepo+":pull") {
			f.t.Errorf("token request scope wrong: %q", r.URL.Query().Get("scope"))
		}
		user, _, ok := r.BasicAuth()
		if !ok || user == "" {
			f.t.Error("token request without robot basic auth")
		}
		w.Write([]byte(`{"token":"fake-registry-token"}`))
	})
	mux.HandleFunc("/v2/ghcr-proxy/"+testRepo+"/manifests/"+testTag, func(w http.ResponseWriter, r *http.Request) {
		f.primePulls.Add(1)
		if r.Header.Get("Authorization") != "Bearer fake-registry-token" {
			f.t.Errorf("manifest pull without the registry Bearer token: %q", r.Header.Get("Authorization"))
		}
		if !strings.Contains(r.Header.Get("Accept"), "oci.image.manifest") {
			f.t.Errorf("manifest pull missing OCI Accept header")
		}
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("POST /api/v2.0/projects/ghcr-proxy/repositories/"+testEscRepo+"/artifacts/"+testTag+"/scan",
		func(w http.ResponseWriter, r *http.Request) {
			f.scanPosts.Add(1)
			w.WriteHeader(http.StatusAccepted)
		})
	mux.HandleFunc("GET /api/v2.0/projects/ghcr-proxy/repositories/"+testEscRepo+"/artifacts/"+testTag+"/additions/vulnerabilities",
		func(w http.ResponseWriter, r *http.Request) {
			if f.reportCode != 0 && f.reportCode != http.StatusOK {
				w.WriteHeader(f.reportCode)
				return
			}
			w.Write([]byte(f.reportBody))
		})
	mux.HandleFunc("GET /api/v2.0/projects/ghcr-proxy/repositories/"+testEscRepo+"/artifacts/"+testTag,
		func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("with_scan_overview") != "true" {
				f.t.Errorf("overview GET without with_scan_overview=true")
			}
			n := f.overviewIdx.Add(1) - 1
			if n < f.overview404 {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			i := int(n - f.overview404)
			if i >= len(f.statuses) {
				i = len(f.statuses) - 1
			}
			status := f.statuses[i]
			ov := map[string]any{
				"scan_overview": map[string]any{
					vulnerabilityReportMIME: map[string]any{
						"report_id":   "r-1",
						"scan_status": status,
						"severity":    "High",
						"summary": map[string]any{
							"total":   7,
							"fixable": 2,
							"summary": map[string]int{"Critical": 0, "High": 2, "Medium": 5},
						},
					},
				},
			}
			json.NewEncoder(w).Encode(ov)
		})
	return mux
}

func newFakeClient(t *testing.T, f *fakeHarbor) *Client {
	t.Helper()
	srv := httptest.NewServer(f.handler())
	t.Cleanup(srv.Close)
	return NewClient(srv.URL+"/api/v2.0", srv.URL, "ghcr-proxy", "robot$ghcr-proxy+e2e-scan-gate", "s3cret-token")
}

func testOptions() Options {
	return Options{PollInterval: time.Millisecond, ScanTimeout: 30 * time.Second}
}

const twoVulnReport = `{"` + vulnerabilityReportMIME + `":{"vulnerabilities":[` +
	`{"id":"CVE-2099-0001","severity":"Critical"},` +
	`{"id":"CVE-2099-0002","severity":"High"}]}}`

func TestScanImagePass(t *testing.T) {
	f := &fakeHarbor{t: t, statuses: []string{"Running", "Success"}, reportBody: twoVulnReport}
	c := newFakeClient(t, f)
	dir := t.TempDir()

	res := ScanImage(context.Background(), c, testOptions(),
		imageSpec{Name: "inari-server", Repo: testRepo, Tag: testTag}, dir)

	if res.Err != nil {
		t.Fatalf("unexpected error: %v", res.Err)
	}
	if !res.Scanned || res.Critical != 0 || res.High != 2 {
		t.Fatalf("unexpected result: %+v", res)
	}
	if f.primePulls.Load() != 1 {
		t.Fatalf("expected exactly one priming pull, got %d", f.primePulls.Load())
	}
	if f.scanPosts.Load() != 1 {
		t.Fatalf("expected exactly one scan POST, got %d", f.scanPosts.Load())
	}
	body, err := os.ReadFile(filepath.Join(dir, "inari-server-1.2.3-vulnerabilities.json"))
	if err != nil || string(body) != twoVulnReport {
		t.Fatalf("report not archived verbatim: %v", err)
	}
}

func TestSummaryCountsArePrimary(t *testing.T) {
	// The overview summary (0 critical / 2 high) disagrees with the report
	// body (1 critical / 1 high) on purpose: Harbor's own summary number is
	// the primary source, so project CVE allowlists stay a Harbor-side knob.
	f := &fakeHarbor{t: t, statuses: []string{"Success"}, reportBody: twoVulnReport}
	c := newFakeClient(t, f)

	res := ScanImage(context.Background(), c, testOptions(),
		imageSpec{Name: "inari-server", Repo: testRepo, Tag: testTag}, t.TempDir())

	if res.Err != nil {
		t.Fatalf("unexpected error: %v", res.Err)
	}
	if res.Critical != 0 || res.High != 2 {
		t.Fatalf("summary counts not used: %+v", res)
	}
}

func TestScanErrorRetriggerOnce(t *testing.T) {
	f := &fakeHarbor{t: t, statuses: []string{"Error", "Running", "Success"}, reportBody: twoVulnReport}
	c := newFakeClient(t, f)

	res := ScanImage(context.Background(), c, testOptions(),
		imageSpec{Name: "inari-server", Repo: testRepo, Tag: testTag}, t.TempDir())

	if res.Err != nil {
		t.Fatalf("unexpected error: %v", res.Err)
	}
	if f.scanPosts.Load() != 2 {
		t.Fatalf("expected one retrigger (2 POSTs), got %d", f.scanPosts.Load())
	}
}

func TestScanErrorPersistentFails(t *testing.T) {
	f := &fakeHarbor{t: t, statuses: []string{"Error", "Error", "Error"}, reportBody: twoVulnReport}
	c := newFakeClient(t, f)

	res := ScanImage(context.Background(), c, testOptions(),
		imageSpec{Name: "inari-server", Repo: testRepo, Tag: testTag}, t.TempDir())

	if res.Err == nil || !strings.Contains(res.Err.Error(), "after one retrigger") {
		t.Fatalf("expected persistent-error failure, got %+v", res)
	}
	if f.scanPosts.Load() != 2 {
		t.Fatalf("expected exactly one retrigger, got %d POSTs", f.scanPosts.Load())
	}
	if strings.Contains(res.Err.Error(), "s3cret-token") {
		t.Fatal("error leaks the robot token")
	}
}

func TestArtifact404Reprimes(t *testing.T) {
	f := &fakeHarbor{t: t, statuses: []string{"Success"}, overview404: 2, reportBody: twoVulnReport}
	c := newFakeClient(t, f)

	res := ScanImage(context.Background(), c, testOptions(),
		imageSpec{Name: "inari-server", Repo: testRepo, Tag: testTag}, t.TempDir())

	if res.Err != nil {
		t.Fatalf("unexpected error: %v", res.Err)
	}
	if f.primePulls.Load() != 3 { // initial prime + one re-prime per 404
		t.Fatalf("expected 3 priming pulls, got %d", f.primePulls.Load())
	}
}

func TestFallbackCountingFromReport(t *testing.T) {
	// Overview reaches Success but carries no severity summary → count from
	// the full report.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/service/token" {
			w.Write([]byte(`{"token":"fake"}`))
			return
		}
		if strings.HasSuffix(r.URL.Path, "/additions/vulnerabilities") {
			w.Write([]byte(twoVulnReport))
			return
		}
		if strings.HasSuffix(r.URL.Path, "/scan") {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		if strings.Contains(r.URL.Path, "/manifests/") {
			w.WriteHeader(http.StatusOK)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"scan_overview": map[string]any{
				vulnerabilityReportMIME: map[string]any{"report_id": "r-1", "scan_status": "Success"},
			},
		})
	}))
	t.Cleanup(srv.Close)
	c := NewClient(srv.URL+"/api/v2.0", srv.URL, "ghcr-proxy", "u", "p")

	res := ScanImage(context.Background(), c, testOptions(),
		imageSpec{Name: "inari-server", Repo: testRepo, Tag: testTag}, t.TempDir())

	if res.Err != nil {
		t.Fatalf("unexpected error: %v", res.Err)
	}
	if res.Critical != 1 || res.High != 1 {
		t.Fatalf("fallback counting wrong: %+v", res)
	}
}

func TestPrimeFailureFailsImage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	c := NewClient(srv.URL+"/api/v2.0", srv.URL, "ghcr-proxy", "u", "s3cret-token")

	res := ScanImage(context.Background(), c, testOptions(),
		imageSpec{Name: "inari-server", Repo: testRepo, Tag: "does-not-exist"}, t.TempDir())

	if res.Err == nil || !strings.Contains(res.Err.Error(), "prime proxy cache") {
		t.Fatalf("expected prime failure, got %+v", res)
	}
	if strings.Contains(res.Err.Error(), "s3cret-token") {
		t.Fatal("error leaks the robot token")
	}
}

func TestArtifactPathDoubleEncoding(t *testing.T) {
	c := NewClient("https://harbor.example/api/v2.0", "https://harbor.example", "ghcr-proxy", "u", "p")
	got := c.artifactPath(testRepo, testTag)
	want := "https://harbor.example/api/v2.0/projects/ghcr-proxy/repositories/7k-inari%252Finari-server/artifacts/1.2.3"
	if got != want {
		t.Fatalf("artifactPath:\n got %s\nwant %s", got, want)
	}
}

func TestNormalizeAgentTag(t *testing.T) {
	cases := map[string]string{
		"0.6.0":         "v0.6.0", // bare stable → prefix
		"v0.6.0":        "v0.6.0", // already prefixed
		"edge":          "edge",
		"0.6.0-c74e714": "0.6.0-c74e714", // per-commit edge build
		"4.0.0-fc36dbf": "4.0.0-fc36dbf",
		"":              "",
	}
	for in, want := range cases {
		if got := NormalizeAgentTag(in); got != want {
			t.Errorf("NormalizeAgentTag(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestImagesFromEnv(t *testing.T) {
	env := map[string]string{
		"SERVER_IMAGE": "4.0.0", "CONSOLE_IMAGE": "0.4.0", "AGENT_IMAGE": "0.6.0",
	}
	images, missing := imagesFromEnv(func(k string) string { return env[k] })
	if len(missing) != 0 {
		t.Fatalf("unexpected missing: %v", missing)
	}
	if images[2].Tag != "v0.6.0" {
		t.Fatalf("agent tag not normalized: %q", images[2].Tag)
	}
	images, missing = imagesFromEnv(func(string) string { return "" })
	if len(missing) != 3 {
		t.Fatalf("expected 3 missing, got %v", missing)
	}
}

func TestParseThreshold(t *testing.T) {
	if n, err := parseThreshold(""); err != nil || n != 0 {
		t.Fatalf("empty default: %d %v", n, err)
	}
	if n, err := parseThreshold("3"); err != nil || n != 3 {
		t.Fatalf("3: %d %v", n, err)
	}
	if _, err := parseThreshold("-1"); err == nil {
		t.Fatal("negative accepted")
	}
	if _, err := parseThreshold("soon"); err == nil {
		t.Fatal("non-integer accepted")
	}
}

func TestReportFileName(t *testing.T) {
	got := reportFileName(imageSpec{Name: "inari-server", Tag: "1.2.3"})
	if got != "inari-server-1.2.3-vulnerabilities.json" {
		t.Fatalf("unexpected file name %q", got)
	}
	if strings.Contains(got, "/") {
		t.Fatal("file name contains a path separator")
	}
}
