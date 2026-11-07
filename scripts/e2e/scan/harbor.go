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

// Client talks to Harbor's v2.0 API and the registry endpoint with robot
// account basic auth. The token never appears in returned errors or logs.
type Client struct {
	api      string // e.g. https://harbor.7kgroup.org/api/v2.0
	registry string // e.g. https://harbor.7kgroup.org
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
		hc:       &http.Client{Timeout: 30 * time.Second},
	}
}

// artifactPath builds the API path for an artifact. The Harbor API requires
// the repository name's slash to be double-URL-encoded (%252F) — a single
// encoding is decoded back to a path separator by the API router and 404s.
func (c *Client) artifactPath(repo, tag string) string {
	escRepo := strings.ReplaceAll(url.PathEscape(repo), "%2F", "%252F")
	return fmt.Sprintf("%s/projects/%s/repositories/%s/artifacts/%s",
		c.api, url.PathEscape(c.project), escRepo, url.PathEscape(tag))
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

// PrimeManifest performs a manifest-only pull through the proxy-cache project
// so the artifact is registered in Harbor before any API call touches it: a
// proxy-cache artifact exists only after something pulls it through.
func (c *Client) PrimeManifest(ctx context.Context, repo, tag string) error {
	tok, err := c.registryToken(ctx, repo)
	if err != nil {
		return err
	}
	u := fmt.Sprintf("%s/v2/%s/%s/manifests/%s",
		c.registry, url.PathEscape(c.project), repo, url.PathEscape(tag))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", manifestAccept)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := c.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("manifest pull through the %s proxy: HTTP %d", c.project, resp.StatusCode)
	}
	return nil
}

// TriggerScan asks Harbor to (re)scan the artifact. 202 Accepted is the
// documented response; any 2xx is accepted. The gate never trusts a prior
// scan's freshness — the explicit POST is the freshness mechanism (scan-on-
// push latency races the poll otherwise).
func (c *Client) TriggerScan(ctx context.Context, repo, tag string) error {
	code, _, err := c.do(ctx, http.MethodPost, c.artifactPath(repo, tag)+"/scan")
	if err != nil {
		return err
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
func (c *Client) GetScanOverview(ctx context.Context, repo, tag string) (int, scanOverview, error) {
	var ov scanOverview
	code, body, err := c.do(ctx, http.MethodGet, c.artifactPath(repo, tag)+"?with_scan_overview=true")
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
func (c *Client) GetVulnerabilityReport(ctx context.Context, repo, tag string) ([]byte, error) {
	code, body, err := c.do(ctx, http.MethodGet, c.artifactPath(repo, tag)+"/additions/vulnerabilities")
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
// fail the gate loudly. HTTP 404 mid-poll re-primes the proxy cache (the
// priming pull raced artifact registration).
func (c *Client) scanAndWait(ctx context.Context, repo, tag string, pollInterval time.Duration) (scanOverview, error) {
	var ov scanOverview
	if err := c.TriggerScan(ctx, repo, tag); err != nil {
		return ov, fmt.Errorf("trigger scan: %w", err)
	}
	retriggered := false
	consecutiveBad := 0
	for {
		code, got, err := c.GetScanOverview(ctx, repo, tag)
		switch {
		case err != nil:
			consecutiveBad++
			if consecutiveBad >= 3 {
				return ov, fmt.Errorf("poll scan overview: %w", err)
			}
		case code == http.StatusNotFound:
			consecutiveBad++
			if consecutiveBad >= 3 {
				return ov, fmt.Errorf("artifact not registered in the %s proxy after re-priming", c.project)
			}
			if perr := c.PrimeManifest(ctx, repo, tag); perr != nil {
				return ov, fmt.Errorf("re-prime proxy cache: %w", perr)
			}
		case code != http.StatusOK:
			consecutiveBad++
			if consecutiveBad >= 3 {
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
				if err := c.TriggerScan(ctx, repo, tag); err != nil {
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
