package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// imageSpec is one candidate image under test. Repo is the full repository
// path inside the Harbor proxy-cache project; Name is the display/file stem.
type imageSpec struct {
	Name string
	Repo string
	Tag  string
}

// PlatformResult is the scan outcome for one digest of an image (one per
// platform sub-manifest; exactly one for single-manifest images).
type PlatformResult struct {
	Platform string // "linux/amd64"; "" for single-manifest images
	Digest   string
	Critical int
	High     int
	Scanned  bool // reached scan_status=Success
	// Unscannable marks Harbor's "scanner does not support this media type"
	// refusal (ORAS bundle artifacts). A loud non-blocking pass, distinct
	// from Err: the gate could run, the scanner simply has nothing to scan.
	Unscannable bool
	Err         error
}

// ImageResult is the outcome for one image. Err marks an image-level
// infrastructure/configuration failure (tag resolution); per-platform
// failures live in Platforms. Critical/High aggregate the platforms.
type ImageResult struct {
	Name      string
	Repo      string
	Tag       string
	Platforms []PlatformResult
	Critical  int
	High      int
	Scanned   bool // every platform reached scan_status=Success
	Err       error
}

// Breached reports whether the result fails the gate: infrastructure error
// at any level, or the aggregate Critical count exceeding the threshold.
// Unscannable platforms never breach — they are a loud pass (their testcase
// name carries the reason into ReportPortal).
func (r ImageResult) Breached(threshold int) bool {
	if r.Err != nil {
		return true
	}
	for _, p := range r.Platforms {
		if p.Err != nil {
			return true
		}
	}
	return r.Critical > threshold
}

// AllUnscannable reports whether every scannable target of the image was
// refused by the scanner as an unsupported media type (the inari-ui-bundle
// ORAS artifact is the live case). Such images pass the gate loudly — the
// JUnit testcase name carries the reason.
func (r ImageResult) AllUnscannable() bool {
	if r.Err != nil || len(r.Platforms) == 0 {
		return false
	}
	for _, p := range r.Platforms {
		if p.Err != nil || !p.Unscannable {
			return false
		}
	}
	return true
}

// Options tunes the scan loop (tests shrink these to milliseconds).
type Options struct {
	PollInterval time.Duration
	ScanTimeout  time.Duration // per image (resolve + register + scan)
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

// reportFileName is the S3-side artifact name for one platform's full report.
func reportFileName(im imageSpec, p PlatformResult) string {
	stem := p.Platform
	if stem == "" {
		stem = "image"
	}
	return fmt.Sprintf("%s-%s-%s-vulnerabilities.json",
		im.Name, im.Tag, strings.ReplaceAll(stem, "/", "-"))
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

// scanPlatform runs the per-digest flow: register the artifact in the proxy
// cache, trigger the scan, poll to Success, archive the report, count.
func scanPlatform(ctx context.Context, c *Client, opts Options, regToken string, im imageSpec, tgt scanTarget, reportsDir string, logf func(string, ...any)) PlatformResult {
	res := PlatformResult{Platform: tgt.Platform, Digest: tgt.Digest}
	label := tgt.Platform
	if label == "" {
		label = "image"
	}

	if err := c.PrimeTarget(ctx, regToken, im.Repo, tgt, opts.PollInterval); err != nil {
		res.Err = err
		return res
	}
	logf("  %s: artifact %s registered", label, tgt.Digest[:19])

	ov, err := c.scanAndWait(ctx, im.Repo, tgt.Digest, opts.PollInterval)
	if err != nil {
		if errors.Is(err, ErrUnscannable) {
			res.Unscannable = true
			logf("  %s: UNSCANNABLE — scanner does not support this media type "+
				"(ORAS bundle artifact — JS deps owned by inari-ui CI); loud non-blocking pass", label)
			return res
		}
		res.Err = err
		return res
	}
	res.Scanned = true

	body, err := c.GetVulnerabilityReport(ctx, im.Repo, tgt.Digest)
	if err != nil {
		res.Err = fmt.Errorf("fetch vulnerability report: %w", err)
		return res
	}
	path := filepath.Join(reportsDir, reportFileName(im, res))
	if err := os.WriteFile(path, body, 0o644); err != nil {
		res.Err = fmt.Errorf("archive vulnerability report: %w", err)
		return res
	}
	res.Critical, res.High = counts(ov, parseReport(body))
	logf("  %s: %d critical, %d high", label, res.Critical, res.High)
	return res
}

// ScanImage runs the full per-image flow: resolve the tag to its scannable
// digests (per-platform for indexes), then scan them concurrently and
// aggregate.
func ScanImage(ctx context.Context, c *Client, opts Options, im imageSpec, reportsDir string, logf func(string, ...any)) ImageResult {
	res := ImageResult{Name: im.Name, Repo: im.Repo, Tag: im.Tag}
	ctx, cancel := context.WithTimeout(ctx, opts.ScanTimeout)
	defer cancel()

	regToken, err := c.registryToken(ctx, im.Repo)
	if err != nil {
		res.Err = fmt.Errorf("registry token: %w", err)
		return res
	}
	targets, err := c.ResolveTargets(ctx, regToken, im.Repo, im.Tag)
	if err != nil {
		res.Err = err
		return res
	}
	logf("  resolved %s:%s → %d scannable digest(s)", im.Repo, im.Tag, len(targets))

	res.Platforms = make([]PlatformResult, len(targets))
	var wg sync.WaitGroup
	for i, tgt := range targets {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res.Platforms[i] = scanPlatform(ctx, c, opts, regToken, im, tgt, reportsDir, logf)
		}()
	}
	wg.Wait()

	res.Scanned = true
	for _, p := range res.Platforms {
		if p.Err != nil || !p.Scanned {
			res.Scanned = false
		}
		res.Critical += p.Critical
		res.High += p.High
	}
	return res
}
