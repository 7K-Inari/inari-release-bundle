package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeHarbor is a scriptable Harbor stand-in for the per-digest scan flow:
// registry token → manifest/index pulls (+ blob kicks) → artifact
// registration poll → scan trigger → overview poll → report fetch.
type fakeHarbor struct {
	t *testing.T

	tagBody   string // JSON served at /v2/.../manifests/<tag>
	tagDigest string // Docker-Content-Digest for the tag GET
	subBodies map[string]string

	registerAfter int32               // artifact GETs (no query) that 404 before 200
	regGets       sync.Map            // ref → *atomic.Int32
	statusSeq     map[string][]string // ref → overview status sequence
	statusIdx     sync.Map            // ref → *atomic.Int32
	summary       map[string][2]int   // ref → (critical, high)

	scanPosts  atomic.Int32
	blobKicks  atomic.Int32
	reportBody string
}

const testRepo = "7k-inari/inari-server"
const testTag = "2.0.0"
const testEscRepo = "7k-inari%252Finari-server"

const testReport = `{"` + vulnerabilityReportMIME + `":{"vulnerabilities":[` +
	`{"id":"CVE-2099-0001","severity":"Critical"},` +
	`{"id":"CVE-2099-0002","severity":"High"}]}}`

func (f *fakeHarbor) artifactHandler(w http.ResponseWriter, r *http.Request, ref string) {
	if r.Method == http.MethodPost || strings.HasSuffix(r.URL.Path, "/scan") {
		f.scanPosts.Add(1)
		w.WriteHeader(http.StatusAccepted)
		return
	}
	if strings.HasSuffix(r.URL.Path, "/additions/vulnerabilities") {
		w.Write([]byte(f.reportBody))
		return
	}
	if r.URL.Query().Get("with_scan_overview") == "true" {
		idx, _ := f.statusIdx.LoadOrStore(ref, &atomic.Int32{})
		n := idx.(*atomic.Int32).Add(1) - 1
		seq := f.statusSeq[ref]
		if len(seq) == 0 {
			seq = []string{"Success"}
		}
		i := int(n)
		if i >= len(seq) {
			i = len(seq) - 1
		}
		crit, high := f.summary[ref][0], f.summary[ref][1]
		json.NewEncoder(w).Encode(map[string]any{
			"scan_overview": map[string]any{
				vulnerabilityReportMIME: map[string]any{
					"report_id":   "r-1",
					"scan_status": seq[i],
					"summary": map[string]any{
						"total":   crit + high,
						"fixable": 0,
						"summary": map[string]int{"Critical": crit, "High": high},
					},
				},
			},
		})
		return
	}
	// Registration poll.
	c, _ := f.regGets.LoadOrStore(ref, &atomic.Int32{})
	if c.(*atomic.Int32).Add(1) <= f.registerAfter {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	w.Write([]byte(`{"digest":"` + ref + `"}`))
}

func (f *fakeHarbor) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/service/token", func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Query().Get("scope"), "repository:ghcr-proxy/"+testRepo+":pull") {
			f.t.Errorf("token request scope wrong: %q", r.URL.Query().Get("scope"))
		}
		if user, _, ok := r.BasicAuth(); !ok || user == "" {
			f.t.Error("token request without robot basic auth")
		}
		w.Write([]byte(`{"token":"fake-registry-token"}`))
	})
	mux.HandleFunc("/v2/ghcr-proxy/"+testRepo+"/manifests/", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer fake-registry-token" {
			f.t.Errorf("manifest pull without the registry Bearer token: %q", r.Header.Get("Authorization"))
		}
		ref := strings.TrimPrefix(r.URL.Path, "/v2/ghcr-proxy/"+testRepo+"/manifests/")
		if body, ok := f.subBodies[ref]; ok {
			w.Header().Set("Docker-Content-Digest", ref)
			w.Write([]byte(body))
			return
		}
		if ref == testTag {
			w.Header().Set("Docker-Content-Digest", f.tagDigest)
			w.Write([]byte(f.tagBody))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	})
	mux.HandleFunc("/v2/ghcr-proxy/"+testRepo+"/blobs/", func(w http.ResponseWriter, r *http.Request) {
		f.blobKicks.Add(1)
		w.WriteHeader(http.StatusPartialContent)
	})
	mux.HandleFunc("/api/v2.0/projects/ghcr-proxy/repositories/"+testEscRepo+"/artifacts/", func(w http.ResponseWriter, r *http.Request) {
		// r.URL.Path is decoded once (7k-inari%2F...), so split on the
		// stable marker instead of trimming the encoded prefix.
		i := strings.Index(r.URL.Path, "/artifacts/")
		ref := r.URL.Path[i+len("/artifacts/"):]
		ref = strings.TrimSuffix(strings.TrimSuffix(ref, "/scan"), "/additions/vulnerabilities")
		f.artifactHandler(w, r, ref)
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

func testImage() imageSpec {
	return imageSpec{Name: "inari-server", Repo: testRepo, Tag: testTag}
}

func discardLog(string, ...any) {}

const subAmd64 = "sha256:aaaa000000000000000000000000000000000000000000000000000000000001"
const subArm64 = "sha256:aaaa000000000000000000000000000000000000000000000000000000000002"
const subAttest = "sha256:aaaa000000000000000000000000000000000000000000000000000000000003"
const idxDigest = "sha256:bbbb000000000000000000000000000000000000000000000000000000000001"

func indexJSON() string {
	return fmt.Sprintf(`{"mediaType":"application/vnd.oci.image.index.v1+json","manifests":[
		{"digest":"%s","mediaType":"application/vnd.oci.image.manifest.v1+json","platform":{"os":"linux","architecture":"amd64"}},
		{"digest":"%s","mediaType":"application/vnd.oci.image.manifest.v1+json","platform":{"os":"linux","architecture":"arm64"}},
		{"digest":"%s","mediaType":"application/vnd.oci.image.manifest.v1+json","platform":{"os":"unknown","architecture":"unknown"}}
	]}`, subAmd64, subArm64, subAttest)
}

const subManifestJSON = `{"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"digest":"sha256:c1"},"layers":[{"digest":"sha256:l1"},{"digest":"sha256:l2"}]}`

func multiArchFake(t *testing.T) *fakeHarbor {
	return &fakeHarbor{
		t:          t,
		tagBody:    indexJSON(),
		tagDigest:  idxDigest,
		subBodies:  map[string]string{subAmd64: subManifestJSON, subArm64: subManifestJSON},
		summary:    map[string][2]int{subAmd64: {0, 2}, subArm64: {1, 3}},
		reportBody: testReport,
	}
}

func TestScanImageMultiArchAggregatesPlatforms(t *testing.T) {
	f := multiArchFake(t)
	c := newFakeClient(t, f)
	dir := t.TempDir()

	res := ScanImage(context.Background(), c, testOptions(), testImage(), dir, discardLog)

	if res.Err != nil {
		t.Fatalf("unexpected error: %v", res.Err)
	}
	if len(res.Platforms) != 2 {
		t.Fatalf("expected 2 platform targets (attestation skipped), got %+v", res.Platforms)
	}
	if !res.Scanned || res.Critical != 1 || res.High != 5 {
		t.Fatalf("unexpected aggregate: %+v", res)
	}
	// One scan POST per platform digest.
	if f.scanPosts.Load() != 2 {
		t.Fatalf("expected 2 scan POSTs, got %d", f.scanPosts.Load())
	}
	// Blob kicks happened (config + 2 layers per platform).
	if f.blobKicks.Load() != 6 {
		t.Fatalf("expected 6 blob kicks, got %d", f.blobKicks.Load())
	}
	// Per-platform reports archived.
	for _, plat := range []string{"linux-amd64", "linux-arm64"} {
		p := filepath.Join(dir, "inari-server-2.0.0-"+plat+"-vulnerabilities.json")
		body, err := os.ReadFile(p)
		if err != nil || string(body) != testReport {
			t.Fatalf("report %s not archived verbatim: %v", p, err)
		}
	}
}

func TestScanImageSingleManifest(t *testing.T) {
	f := &fakeHarbor{
		t:          t,
		tagBody:    subManifestJSON,
		tagDigest:  idxDigest,
		subBodies:  map[string]string{idxDigest: subManifestJSON},
		summary:    map[string][2]int{idxDigest: {0, 4}},
		reportBody: testReport,
	}
	c := newFakeClient(t, f)

	res := ScanImage(context.Background(), c, testOptions(), testImage(), t.TempDir(), discardLog)

	if res.Err != nil {
		t.Fatalf("unexpected error: %v", res.Err)
	}
	if len(res.Platforms) != 1 || res.Platforms[0].Digest != idxDigest {
		t.Fatalf("expected one digest target, got %+v", res.Platforms)
	}
	if res.Critical != 0 || res.High != 4 {
		t.Fatalf("unexpected counts: %+v", res)
	}
}

func TestRegistrationPollWaits(t *testing.T) {
	f := multiArchFake(t)
	f.registerAfter = 3 // artifact GETs 404 three times before registering
	c := newFakeClient(t, f)

	res := ScanImage(context.Background(), c, testOptions(), testImage(), t.TempDir(), discardLog)

	if res.Err != nil || !res.Scanned {
		t.Fatalf("registration poll should have waited: %+v", res)
	}
}

func TestRegistrationTimeout(t *testing.T) {
	f := multiArchFake(t)
	f.registerAfter = 1 << 30 // never registers
	c := newFakeClient(t, f)
	opts := Options{PollInterval: time.Millisecond, ScanTimeout: 50 * time.Millisecond}

	res := ScanImage(context.Background(), c, opts, testImage(), t.TempDir(), discardLog)

	if res.Err == nil && res.Platforms[0].Err == nil {
		t.Fatalf("expected registration timeout, got %+v", res)
	}
	err := res.Platforms[0].Err
	if err == nil || !strings.Contains(err.Error(), "not registered") {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(err.Error(), "s3cret-token") {
		t.Fatal("error leaks the robot token")
	}
}

func TestScanErrorRetriggerOnce(t *testing.T) {
	f := multiArchFake(t)
	f.statusSeq = map[string][]string{subAmd64: {"Error", "Running", "Success"}}
	c := newFakeClient(t, f)

	res := ScanImage(context.Background(), c, testOptions(), testImage(), t.TempDir(), discardLog)

	if res.Err != nil || !res.Scanned {
		t.Fatalf("unexpected failure: %+v", res)
	}
	// 2 POSTs for the amd64 retrigger + 1 for arm64.
	if f.scanPosts.Load() != 3 {
		t.Fatalf("expected 3 scan POSTs, got %d", f.scanPosts.Load())
	}
}

func TestScanErrorPersistentFails(t *testing.T) {
	f := multiArchFake(t)
	f.statusSeq = map[string][]string{subAmd64: {"Error", "Error", "Error"}}
	c := newFakeClient(t, f)

	res := ScanImage(context.Background(), c, testOptions(), testImage(), t.TempDir(), discardLog)

	if res.Scanned {
		t.Fatalf("expected failure, got %+v", res)
	}
	var perr error
	for _, p := range res.Platforms {
		if p.Err != nil {
			perr = p.Err
		}
	}
	if perr == nil || !strings.Contains(perr.Error(), "after one retrigger") {
		t.Fatalf("expected persistent-error failure, got %+v", res.Platforms)
	}
	if strings.Contains(perr.Error(), "s3cret-token") {
		t.Fatal("error leaks the robot token")
	}
}

func TestFallbackCountingFromReport(t *testing.T) {
	// Overview reaches Success but carries no severity summary → count from
	// the full report (1 critical, 1 high in testReport).
	f := &fakeHarbor{
		t:          t,
		tagBody:    subManifestJSON,
		tagDigest:  idxDigest,
		subBodies:  map[string]string{idxDigest: subManifestJSON},
		reportBody: testReport,
	}
	// Wrapper server drops the summary from the overview response.
	noSummary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/artifacts/") && r.URL.Query().Get("with_scan_overview") == "true" {
			json.NewEncoder(w).Encode(map[string]any{
				"scan_overview": map[string]any{
					vulnerabilityReportMIME: map[string]any{"report_id": "r-1", "scan_status": "Success"},
				},
			})
			return
		}
		f.handler().ServeHTTP(w, r)
	}))
	defer noSummary.Close()
	c := NewClient(noSummary.URL+"/api/v2.0", noSummary.URL, "ghcr-proxy", "u", "p")

	res := ScanImage(context.Background(), c, testOptions(), testImage(), t.TempDir(), discardLog)

	if res.Err != nil {
		t.Fatalf("unexpected error: %v", res.Err)
	}
	if res.Critical != 1 || res.High != 1 {
		t.Fatalf("fallback counting wrong: %+v", res)
	}
}

func TestResolveTargetsSkipsAttestations(t *testing.T) {
	f := multiArchFake(t)
	c := newFakeClient(t, f)
	tok, err := c.registryToken(context.Background(), testRepo)
	if err != nil {
		t.Fatal(err)
	}
	targets, err := c.ResolveTargets(context.Background(), tok, testRepo, testTag)
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 2 {
		t.Fatalf("expected 2 targets, got %+v", targets)
	}
	for _, tgt := range targets {
		if tgt.Digest == subAttest {
			t.Fatal("attestation manifest not skipped")
		}
	}
}

func TestArtifactPathDoubleEncoding(t *testing.T) {
	c := NewClient("http://harbor/api/v2.0", "http://harbor", "ghcr-proxy", "u", "p")
	got := c.artifactPath(testRepo, testTag)
	want := "http://harbor/api/v2.0/projects/ghcr-proxy/repositories/7k-inari%252Finari-server/artifacts/2.0.0"
	if got != want {
		t.Fatalf("artifactPath:\n got %s\nwant %s", got, want)
	}
	// Digests keep their raw colon (Go's PathEscape leaves it; Harbor accepts
	// it — live-verified).
	got = c.artifactPath(testRepo, "sha256:abc")
	if !strings.HasSuffix(got, "/artifacts/sha256:abc") {
		t.Fatalf("digest reference mangled: %s", got)
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
	_, missing = imagesFromEnv(func(string) string { return "" })
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
	got := reportFileName(testImage(), PlatformResult{Platform: "linux/amd64"})
	if got != "inari-server-2.0.0-linux-amd64-vulnerabilities.json" {
		t.Fatalf("unexpected file name %q", got)
	}
	got = reportFileName(testImage(), PlatformResult{})
	if got != "inari-server-2.0.0-image-vulnerabilities.json" {
		t.Fatalf("unexpected single-manifest file name %q", got)
	}
	if strings.Contains(got, "/") {
		t.Fatal("file name contains a path separator")
	}
}
