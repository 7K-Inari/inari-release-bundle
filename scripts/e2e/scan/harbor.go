package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// vulnerabilityReportMIME is the scan_overview key Harbor uses for Trivy
// vulnerability reports.
const vulnerabilityReportMIME = "application/vnd.security.vulnerability.report; version=1.1"

// manifestAccept covers OCI + Docker manifest and index media types so the
// priming pull works for single-arch and multi-arch images alike.
const manifestAccept = "application/vnd.oci.image.manifest.v1+json, " +
	"application/vnd.oci.image.index.v1+json, " +
	"application/vnd.docker.distribution.manifest.v2+json, " +
	"application/vnd.docker.distribution.manifest.list.v2+json"

// Client talks to Harbor's core service: the v2.0 API and the /v2/ registry
// proxy (with /service/token), all with robot account basic auth. The token
// never appears in returned errors or logs.
type Client struct {
	api      string // e.g. http://harbor-core.harbor.svc.cluster.local/api/v2.0
	registry string // e.g. http://harbor-core.harbor.svc.cluster.local
	project  string // e.g. ghcr-proxy
	user     string
	token    string
	hc       *http.Client
}

func NewClient(api, registry, project, user, token string) *Client {
	return &Client{
		api:      strings.TrimSuffix(api, "/"),
		registry: strings.TrimSuffix(registry, "/"),
		project:  project,
		user:     user,
		token:    token,
		// Cold-cache proxy fetches (upstream pull on a busy core) need
		// headroom beyond the stock 30s.
		hc: &http.Client{Timeout: 120 * time.Second},
	}
}

// artifactPath builds the API path for an artifact. The Harbor API requires
// the repository name's slash to be double-URL-encoded (%252F) — a single
// encoding is decoded back to a path separator by the API router and 404s.
// ref may be a tag or a digest (sha256:...).
func (c *Client) artifactPath(repo, ref string) string {
	escRepo := strings.ReplaceAll(url.PathEscape(repo), "%2F", "%252F")
	return fmt.Sprintf("%s/projects/%s/repositories/%s/artifacts/%s",
		c.api, url.PathEscape(c.project), escRepo, url.PathEscape(ref))
}

func (c *Client) do(ctx context.Context, method, u string) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, u, nil)
	if err != nil {
		return 0, nil, err
	}
	req.SetBasicAuth(c.user, c.token)
	resp, err := c.hc.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return 0, nil, err
	}
	return resp.StatusCode, body, nil
}

// --- registry (/v2/) side: priming ----------------------------------------

// registryToken fetches a registry pull token from Harbor's token service
// (hosted on core, next to the /v2/ proxy) with robot basic auth. The e2e
// defaults point at the in-cluster core service, so the whole flow stays on
// service DNS — no gateway/DNS/TLS in the gate path.
func (c *Client) registryToken(ctx context.Context, repo string) (string, error) {
	u := fmt.Sprintf("%s/service/token?service=harbor-registry&scope=%s",
		c.registry, url.QueryEscape("repository:"+c.project+"/"+repo+":pull"))
	code, body, err := c.do(ctx, http.MethodGet, u)
	if err != nil {
		return "", err
	}
	if code != http.StatusOK {
		return "", fmt.Errorf("registry token: HTTP %d", code)
	}
	var tok struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(body, &tok); err != nil {
		return "", fmt.Errorf("parse registry token: %w", err)
	}
	if t := tok.Token; t != "" {
		return t, nil
	}
	if t := tok.AccessToken; t != "" {
		return t, nil
	}
	return "", fmt.Errorf("registry token response carries no token")
}

// ociManifest is the slice of an image manifest / index the gate needs.
type ociManifest struct {
	MediaType string `json:"mediaType"`
	Config    struct {
		Digest string `json:"digest"`
	} `json:"config"`
	Layers []struct {
		Digest string `json:"digest"`
	} `json:"layers"`
	Manifests []struct {
		Digest   string `json:"digest"`
		Platform *struct {
			OS           string `json:"os"`
			Architecture string `json:"architecture"`
		} `json:"platform"`
	} `json:"manifests"`
}

// getManifest fetches a manifest or index by reference (tag or digest)
// through the proxy cache, returning the body and the canonical digest.
// Every GET through the proxy kicks Harbor's background registration of the
// referenced content. One retry: a cold-cache upstream fetch under load
// (concurrent background blob pushes) can outlast the client timeout.
func (c *Client) getManifest(ctx context.Context, regToken, repo, ref string) (*ociManifest, string, error) {
	var lastErr error
	for attempt := range 2 {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, "", lastErr
			case <-time.After(5 * time.Second):
			}
		}
		m, digest, err := c.getManifestOnce(ctx, regToken, repo, ref)
		if err == nil {
			return m, digest, nil
		}
		lastErr = err
	}
	return nil, "", lastErr
}

func (c *Client) getManifestOnce(ctx context.Context, regToken, repo, ref string) (*ociManifest, string, error) {
	u := fmt.Sprintf("%s/v2/%s/%s/manifests/%s",
		c.registry, url.PathEscape(c.project), repo, url.PathEscape(ref))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Accept", manifestAccept)
	req.Header.Set("Authorization", "Bearer "+regToken)
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, "", err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("manifest %s: HTTP %d", ref, resp.StatusCode)
	}
	var m ociManifest
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, "", fmt.Errorf("parse manifest %s: %w", ref, err)
	}
	return &m, resp.Header.Get("Docker-Content-Digest"), nil
}

// kickBlob issues a 1-byte range GET for a blob. Harbor's proxy serves it
// (206) and, on a cache miss, pushes the FULL blob to local storage in a
// background goroutine — this shortens the sub-manifest handler's dependency
// wait (up to 200s without kicks) to seconds.
func (c *Client) kickBlob(ctx context.Context, regToken, repo, digest string) {
	u := fmt.Sprintf("%s/v2/%s/%s/blobs/%s",
		c.registry, url.PathEscape(c.project), repo, url.PathEscape(digest))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return
	}
	req.Header.Set("Authorization", "Bearer "+regToken)
	req.Header.Set("Range", "bytes=0-0")
	resp, err := c.hc.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	// Deliberately truncate: the point is the server-side background push,
	// not the bytes.
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1))
}

// scanTarget is one scannable artifact digest of a candidate image: the
// image manifest itself (single-arch images) or each platform sub-manifest
// of an index (attestations excluded — they carry no packages).
type scanTarget struct {
	Digest   string
	Platform string // "linux/amd64"; "" for a single-manifest image
}

// ResolveTargets resolves a tag to its scannable digests. Multi-arch indexes
// are scanned per platform sub-manifest: Harbor registers pushed
// sub-manifests synchronously-ish, while INDEX/tag registration is
// asynchronous (waits on all children, up to ~7 min, trims or drops on
// timeout) — far too racy for a gate. The CVE content of an index is the
// union of its platform manifests, so per-digest scanning is the same signal
// with deterministic timing.
func (c *Client) ResolveTargets(ctx context.Context, regToken, repo, tag string) ([]scanTarget, error) {
	m, digest, err := c.getManifest(ctx, regToken, repo, tag)
	if err != nil {
		return nil, fmt.Errorf("resolve %s: %w", tag, err)
	}
	if len(m.Manifests) == 0 {
		if digest == "" {
			return nil, fmt.Errorf("resolve %s: no Docker-Content-Digest header", tag)
		}
		return []scanTarget{{Digest: digest}}, nil
	}
	var targets []scanTarget
	for _, sub := range m.Manifests {
		if sub.Platform != nil && sub.Platform.OS == "unknown" && sub.Platform.Architecture == "unknown" {
			continue // attestation manifest
		}
		targets = append(targets, scanTarget{
			Digest:   sub.Digest,
			Platform: sub.Platform.OS + "/" + sub.Platform.Architecture,
		})
	}
	if len(targets) == 0 {
		return nil, fmt.Errorf("resolve %s: index carries no platform manifests", tag)
	}
	return targets, nil
}

// PrimeTarget registers a digest's artifact in the proxy cache: pull the
// manifest by digest (kicks the background push), kick its blobs, then poll
// the artifact API until Harbor registers it.
func (c *Client) PrimeTarget(ctx context.Context, regToken, repo string, tgt scanTarget, pollInterval time.Duration) error {
	m, _, err := c.getManifest(ctx, regToken, repo, tgt.Digest)
	if err != nil {
		return fmt.Errorf("pull sub-manifest: %w", err)
	}
	if m.Config.Digest != "" {
		c.kickBlob(ctx, regToken, repo, m.Config.Digest)
	}
	for _, l := range m.Layers {
		c.kickBlob(ctx, regToken, repo, l.Digest)
	}
	for {
		code, _, err := c.do(ctx, http.MethodGet, c.artifactPath(repo, tgt.Digest))
		if err == nil && code == http.StatusOK {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("artifact %s not registered in the %s proxy before the deadline (last HTTP %d)", tgt.Digest, c.project, code)
		case <-time.After(pollInterval):
		}
	}
}

// --- API side: scan + report ----------------------------------------------

// TriggerScan asks Harbor to (re)scan the artifact. 202 Accepted is the
// documented response; any 2xx is accepted, and 409 (a scan is already
// running — scan-on-push may have beaten us) is fine too. The gate never
// trusts a prior scan's freshness — the explicit POST is the freshness
// mechanism (scan-on-push latency races the poll otherwise).
func (c *Client) TriggerScan(ctx context.Context, repo, ref string) error {
	code, _, err := c.do(ctx, http.MethodPost, c.artifactPath(repo, ref)+"/scan")
	if err != nil {
		return err
	}
	if code == http.StatusConflict {
		return nil
	}
	if code < 200 || code >= 300 {
		return fmt.Errorf("POST scan: HTTP %d", code)
	}
	return nil
}

// scanOverview is the per-report slice of the artifact's scan_overview.
type scanOverview struct {
	ReportID   string `json:"report_id"`
	ScanStatus string `json:"scan_status"`
	Severity   string `json:"severity"`
	Summary    struct {
		Total   int            `json:"total"`
		Fixable int            `json:"fixable"`
		Summary map[string]int `json:"summary"`
	} `json:"summary"`
}

// GetScanOverview reads the artifact with its scan overview. A missing
// report yields a zero overview (ScanStatus ""), which the poll loop treats
// as "not finished". HTTP 404 means the artifact is not registered (yet).
func (c *Client) GetScanOverview(ctx context.Context, repo, ref string) (int, scanOverview, error) {
	var ov scanOverview
	code, body, err := c.do(ctx, http.MethodGet, c.artifactPath(repo, ref)+"?with_scan_overview=true")
	if err != nil {
		return 0, ov, err
	}
	if code != http.StatusOK {
		return code, ov, nil
	}
	var art struct {
		ScanOverview map[string]scanOverview `json:"scan_overview"`
	}
	if err := json.Unmarshal(body, &art); err != nil {
		return code, ov, fmt.Errorf("parse artifact overview: %w", err)
	}
	if got, ok := art.ScanOverview[vulnerabilityReportMIME]; ok {
		ov = got
	}
	return code, ov, nil
}

// vulnReport is the slice of the additions/vulnerabilities response the gate
// falls back to when the overview carries no severity summary.
type vulnReport struct {
	Vulnerabilities []struct {
		ID       string `json:"id"`
		Severity string `json:"severity"`
	} `json:"vulnerabilities"`
}

// GetVulnerabilityReport fetches the full vulnerability report (the
// additions/vulnerabilities addition), returned verbatim for archival.
func (c *Client) GetVulnerabilityReport(ctx context.Context, repo, ref string) ([]byte, error) {
	code, body, err := c.do(ctx, http.MethodGet, c.artifactPath(repo, ref)+"/additions/vulnerabilities")
	if err != nil {
		return nil, err
	}
	if code != http.StatusOK {
		return nil, fmt.Errorf("GET additions/vulnerabilities: HTTP %d", code)
	}
	return body, nil
}

// parseReport extracts the vulnerability list from the additions response,
// which is keyed by report MIME type. Returns nil when unparseable.
func parseReport(body []byte) *vulnReport {
	var byMIME map[string]vulnReport
	if err := json.Unmarshal(body, &byMIME); err != nil {
		return nil
	}
	if rep, ok := byMIME[vulnerabilityReportMIME]; ok {
		return &rep
	}
	for _, rep := range byMIME { // any single report is better than none
		r := rep
		return &r
	}
	return nil
}

// scanAndWait triggers a scan and polls until scan_status=Success. Terminal
// scanner errors (Error/Stopped) get exactly one retrigger — persistent
// failures usually mean the Trivy scanner is broken (DB egress), which must
// fail the gate loudly. HTTP 404 mid-poll means registration is still
// catching up — keep waiting (the context bounds it).
func (c *Client) scanAndWait(ctx context.Context, repo, ref string, pollInterval time.Duration) (scanOverview, error) {
	var ov scanOverview
	if err := c.TriggerScan(ctx, repo, ref); err != nil {
		return ov, fmt.Errorf("trigger scan: %w", err)
	}
	retriggered := false
	consecutiveBad := 0
	for {
		code, got, err := c.GetScanOverview(ctx, repo, ref)
		switch {
		case err != nil:
			consecutiveBad++
			if consecutiveBad >= 6 {
				return ov, fmt.Errorf("poll scan overview: %w", err)
			}
		case code == http.StatusNotFound:
			consecutiveBad++
			if consecutiveBad >= 6 {
				return ov, fmt.Errorf("artifact %s vanished mid-scan (proxy GC?)", ref)
			}
		case code != http.StatusOK:
			consecutiveBad++
			if consecutiveBad >= 6 {
				return ov, fmt.Errorf("poll scan overview: HTTP %d", code)
			}
		default:
			consecutiveBad = 0
			ov = got
			switch ov.ScanStatus {
			case "Success":
				return ov, nil
			case "Error", "Stopped":
				if retriggered {
					return ov, fmt.Errorf("scan ended in status %q after one retrigger — check the Trivy scanner (DB egress?)", ov.ScanStatus)
				}
				retriggered = true
				if err := c.TriggerScan(ctx, repo, ref); err != nil {
					return ov, fmt.Errorf("retrigger scan: %w", err)
				}
			}
			// "Queued" / "Pending" / "Running" / "Not Scanned" / "" → keep polling.
		}
		select {
		case <-ctx.Done():
			return ov, fmt.Errorf("timed out waiting for scan_status=Success: %w", ctx.Err())
		case <-time.After(pollInterval):
		}
	}
}
