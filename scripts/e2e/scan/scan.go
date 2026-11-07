package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// imageSpec is one candidate image under test. Repo is the full repository
// path inside the Harbor proxy-cache project; Name is the display/file stem.
type imageSpec struct {
	Name string
	Repo string
	Tag  string
}

// ImageResult is the outcome for one image. Err marks an infrastructure /
// configuration failure (distinct from a threshold breach, which is a clean
// scan with too many Criticals).
type ImageResult struct {
	Name     string
	Repo     string
	Tag      string
	Critical int
	High     int
	Scanned  bool // reached scan_status=Success
	Err      error
}

// Options tunes the scan loop (tests shrink these to milliseconds).
type Options struct {
	PollInterval time.Duration
	ScanTimeout  time.Duration // per image
}

var bareSemver = regexp.MustCompile(`^\d+\.\d+\.\d+$`)

// NormalizeAgentTag compensates the registry's tag-prefix convention split:
// inari-agent stable images are v-prefixed (v0.5.1) while the workflow's
// agent-image parameter (from the platform chart's agent.recommended) is
// bare. Prefix "v" iff the tag is a bare X.Y.Z; "edge" and per-commit edge
// builds (X.Y.Z-<sha>) stay as-is. Same rule as the cosign-verify step.
func NormalizeAgentTag(tag string) string {
	if strings.HasPrefix(tag, "v") {
		return tag
	}
	if bareSemver.MatchString(tag) {
		return "v" + tag
	}
	return tag
}

// imagesFromEnv maps the workflow's image parameters onto the proxy-cache
// repositories the e2e environment pulls (gitops/e2e/README.md §Image pull
// path). A missing tag is reported via missing so main can fail loud.
func imagesFromEnv(getenv func(string) string) (images []imageSpec, missing []string) {
	images = []imageSpec{
		{Name: "inari-server", Repo: "7k-inari/inari-server", Tag: getenv("SERVER_IMAGE")},
		{Name: "inari-ui-bundle", Repo: "7k-inari/inari-ui-bundle", Tag: getenv("CONSOLE_IMAGE")},
		{Name: "inari-agent", Repo: "7k-inari/inari-agent", Tag: NormalizeAgentTag(getenv("AGENT_IMAGE"))},
	}
	for _, im := range images {
		if im.Tag == "" {
			missing = append(missing, im.Name)
		}
	}
	return images, missing
}

// reportFileName is the S3-side artifact name for the full report.
func reportFileName(im imageSpec) string {
	return fmt.Sprintf("%s-%s-vulnerabilities.json", im.Name, im.Tag)
}

// counts returns (critical, high). Harbor's overview summary is the primary
// source — it is the number Harbor itself reports, so project CVE allowlists
// managed in Harbor stay the single knob (never duplicated in code here).
// The full report is the fallback when the overview carries no summary.
func counts(ov scanOverview, rep *vulnReport) (critical, high int) {
	if ov.Summary.Summary != nil {
		return ov.Summary.Summary["Critical"], ov.Summary.Summary["High"]
	}
	if rep == nil {
		return 0, 0
	}
	for _, v := range rep.Vulnerabilities {
		switch strings.ToLower(v.Severity) {
		case "critical":
			critical++
		case "high":
			high++
		}
	}
	return critical, high
}

// ScanImage runs the full per-image flow: prime the proxy cache, trigger the
// scan, poll to Success, archive the full report, count severities.
func ScanImage(ctx context.Context, c *Client, opts Options, im imageSpec, reportsDir string) ImageResult {
	res := ImageResult{Name: im.Name, Repo: im.Repo, Tag: im.Tag}
	ctx, cancel := context.WithTimeout(ctx, opts.ScanTimeout)
	defer cancel()

	if err := c.PrimeManifest(ctx, im.Repo, im.Tag); err != nil {
		res.Err = fmt.Errorf("prime proxy cache: %w", err)
		return res
	}
	ov, err := c.scanAndWait(ctx, im.Repo, im.Tag, opts.PollInterval)
	if err != nil {
		res.Err = err
		return res
	}
	res.Scanned = true

	body, err := c.GetVulnerabilityReport(ctx, im.Repo, im.Tag)
	if err != nil {
		res.Err = fmt.Errorf("fetch vulnerability report: %w", err)
		return res
	}
	path := filepath.Join(reportsDir, reportFileName(im))
	if err := os.WriteFile(path, body, 0o644); err != nil {
		res.Err = fmt.Errorf("archive vulnerability report: %w", err)
		return res
	}
	res.Critical, res.High = counts(ov, parseReport(body))
	return res
}
