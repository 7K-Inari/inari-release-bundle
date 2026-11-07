package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Harbor/Trivy scan gate (W6) — release mode only. Scans exactly the
// candidate image set (hard rule 5) in the ghcr-proxy proxy-cache project,
// fails when any image's Critical CVE count exceeds SCAN_CRITICAL_THRESHOLD,
// archives the full vulnerability reports, and always writes the synthetic
// `security` JUnit so failed runs still report (hard rule 8/10).
// Complements cosign (provenance), never replaces it. CVE allowlists live in
// Harbor, never here. Pull-blocking stays OFF on ghcr-proxy (hard rule 7).

func main() {
	os.Exit(run(os.Getenv, os.Stdout))
}

func envOr(getenv func(string) string, key, def string) string {
	if v := getenv(key); v != "" {
		return v
	}
	return def
}

func parseThreshold(s string) (int, error) {
	if s == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("SCAN_CRITICAL_THRESHOLD must be a non-negative integer (got %q)", s)
	}
	return n, nil
}

func run(getenv func(string) string, stdout io.Writer) int {
	outDir := envOr(getenv, "OUT_DIR", "/artifacts/out")
	reportsDir := envOr(getenv, "REPORTS_DIR", "/artifacts/reports")
	runID := getenv("SCAN_RUN_ID")

	mustDirs := func() error {
		return errors.Join(
			os.MkdirAll(outDir, 0o755),
			os.MkdirAll(reportsDir, 0o755),
		)
	}

	// failConfig reports a configuration failure through the same JUnit
	// channel as scan failures — a gate that degrades to pass (or to an
	// opaque pod Error with no report) is worse than no gate.
	failConfig := func(err error) int {
		_ = mustDirs()
		res := []ImageResult{{Name: "scan-gate-configuration", Err: err}}
		if werr := WriteJUnit(filepath.Join(outDir, "junit.xml"), res, 0, runID); werr != nil {
			fmt.Fprintf(stdout, "WARNING: could not write junit: %v\n", werr)
		}
		fmt.Fprintf(stdout, "CONFIGURATION ERROR: %v\n", err)
		return 1
	}

	threshold, err := parseThreshold(getenv("SCAN_CRITICAL_THRESHOLD"))
	if err != nil {
		return failConfig(err)
	}

	user, token := getenv("HARBOR_ROBOT_USERNAME"), getenv("HARBOR_ROBOT_TOKEN")
	if user == "" || token == "" {
		return failConfig(errors.New("HARBOR_ROBOT_USERNAME/HARBOR_ROBOT_TOKEN unset — " +
			"the harbor-scanner ESO secret is not synced " +
			"(gitops/e2e/README.md §Harbor scan gate prerequisites)"))
	}

	images, missing := imagesFromEnv(getenv)
	if len(missing) > 0 {
		return failConfig(fmt.Errorf("image tag parameters empty for: %s", strings.Join(missing, ", ")))
	}

	if err := mustDirs(); err != nil {
		fmt.Fprintf(stdout, "CONFIGURATION ERROR: artifact dirs: %v\n", err)
		return 1
	}

	client := NewClient(
		envOr(getenv, "HARBOR_API", "https://harbor.7kgroup.org/api/v2.0"),
		envOr(getenv, "HARBOR_REGISTRY", "https://harbor.7kgroup.org"),
		envOr(getenv, "HARBOR_PROJECT", "ghcr-proxy"),
		user, token,
	)
	opts := Options{
		PollInterval: 15 * time.Second,
		ScanTimeout:  10 * time.Minute,
	}
	if v := getenv("SCAN_POLL_INTERVAL_SECONDS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			opts.PollInterval = time.Duration(n) * time.Second
		}
	}
	if v := getenv("SCAN_TIMEOUT_SECONDS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			opts.ScanTimeout = time.Duration(n) * time.Second
		}
	}

	results := make([]ImageResult, 0, len(images))
	failed := false
	for _, im := range images {
		fmt.Fprintf(stdout, "== scanning %s:%s\n", im.Repo, im.Tag)
		r := ScanImage(context.Background(), client, opts, im, reportsDir)
		results = append(results, r)
		switch {
		case r.Err != nil:
			failed = true
			fmt.Fprintf(stdout, "ERROR %s:%s — %v\n", r.Repo, r.Tag, r.Err)
		case r.Critical > threshold:
			failed = true
			fmt.Fprintf(stdout, "FAIL %s:%s — %d critical (threshold %d), %d high\n",
				r.Repo, r.Tag, r.Critical, threshold, r.High)
		default:
			fmt.Fprintf(stdout, "PASS %s:%s — %d critical, %d high\n",
				r.Repo, r.Tag, r.Critical, r.High)
		}
	}

	if err := WriteJUnit(filepath.Join(outDir, "junit.xml"), results, threshold, runID); err != nil {
		// The JUnit is the gate's reporting channel — fail loud if it
		// cannot be written.
		fmt.Fprintf(stdout, "ERROR: could not write junit: %v\n", err)
		return 1
	}
	if failed {
		fmt.Fprintln(stdout, "== scan gate FAILED")
		return 1
	}
	fmt.Fprintln(stdout, "== scan gate passed")
	return 0
}
