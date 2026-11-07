package main

import (
	"encoding/xml"
	"fmt"
	"os"
	"strings"
)

// JUnit wrapper for the gate (result contract: docs/e2e/test-taxonomy.md §5 —
// the scan leg emits a small synthetic JUnit that the workflow's exit-handler
// imports into the run's ReportPortal launch). The testsuite carries the
// taxonomy's `security` surface label as its name.

type junitTestsuite struct {
	XMLName  xml.Name        `xml:"testsuite"`
	Name     string          `xml:"name,attr"`
	Tests    int             `xml:"tests,attr"`
	Failures int             `xml:"failures,attr"`
	Cases    []junitTestcase `xml:"testcase"`
}

type junitTestcase struct {
	Classname string        `xml:"classname,attr"`
	Name      string        `xml:"name,attr"`
	Failure   *junitFailure `xml:"failure,omitempty"`
}

type junitFailure struct {
	Message string `xml:"message,attr"`
	Body    string `xml:",chardata"`
}

func (r ImageResult) caseName() string {
	name := r.Name
	if r.Repo != "" {
		name = fmt.Sprintf("image-scan %s:%s", r.Repo, r.Tag)
	}
	if r.AllUnscannable() {
		// Coordinator ruling (PR #165): the reason rides in the testcase
		// name so the pass is loud in ReportPortal, never silent.
		name += " — unscannable: ORAS bundle artifact — JS deps owned by inari-ui CI"
	}
	return name
}

func (r ImageResult) failure(threshold int, runID string) *junitFailure {
	if r.Err != nil {
		return &junitFailure{
			Message: fmt.Sprintf("scan gate error: %v", r.Err),
			Body:    "The gate could not complete the scan. See the harbor-scan-gate step log in the Argo workflow.",
		}
	}
	for _, p := range r.Platforms {
		if p.Err != nil {
			label := p.Platform
			if label == "" {
				label = "image"
			}
			return &junitFailure{
				Message: fmt.Sprintf("scan gate error on %s (%s): %v", label, p.Digest, p.Err),
				Body:    "The gate could not complete the scan. See the harbor-scan-gate step log in the Argo workflow.",
			}
		}
	}
	var perPlatform []string
	for _, p := range r.Platforms {
		label := p.Platform
		if label == "" {
			label = "image"
		}
		perPlatform = append(perPlatform, fmt.Sprintf("%s: %d critical, %d high", label, p.Critical, p.High))
	}
	body := "Harbor/Trivy found more Critical vulnerabilities than the gate allows (" +
		strings.Join(perPlatform, "; ") + "). " +
		"CVE allowlists are managed in Harbor (project ghcr-proxy), never in scripts."
	if runID != "" {
		body += fmt.Sprintf(" Full reports: s3://argo-workflows-artifacts/e2e/%s/scan/reports/ (per platform)",
			runID)
	}
	return &junitFailure{
		Message: fmt.Sprintf("%d critical CVE(s) across %d platform manifest(s) exceed the threshold %d",
			r.Critical, len(r.Platforms), threshold),
		Body: body,
	}
}

// WriteJUnit writes the synthetic security testsuite. Called on EVERY outcome
// — including configuration errors — so a failed gate still lands in
// ReportPortal (hard rule 8/10: aborted/failed runs still report).
func WriteJUnit(path string, results []ImageResult, threshold int, runID string) error {
	suite := junitTestsuite{
		Name:  "security",
		Tests: len(results),
	}
	for _, r := range results {
		tc := junitTestcase{
			Classname: "security.harbor-scan",
			Name:      r.caseName(),
		}
		if r.Breached(threshold) {
			suite.Failures++
			tc.Failure = r.failure(threshold, runID)
		}
		suite.Cases = append(suite.Cases, tc)
	}
	out, err := xml.MarshalIndent(suite, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append([]byte(xml.Header), out...), 0o644)
}
