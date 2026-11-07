package main

import (
	"encoding/xml"
	"fmt"
	"os"
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

// Breached reports whether the result fails the gate: infrastructure error,
// or a Critical count exceeding the threshold.
func (r ImageResult) Breached(threshold int) bool {
	return r.Err != nil || r.Critical > threshold
}

func (r ImageResult) caseName() string {
	if r.Repo == "" {
		return r.Name
	}
	return fmt.Sprintf("image-scan %s:%s", r.Repo, r.Tag)
}

func (r ImageResult) failure(threshold int, runID string) *junitFailure {
	if r.Err != nil {
		return &junitFailure{
			Message: fmt.Sprintf("scan gate error: %v", r.Err),
			Body:    "The gate could not complete the scan. See the harbor-scan-gate step log in the Argo workflow.",
		}
	}
	body := "Harbor/Trivy found more Critical vulnerabilities than the gate allows. " +
		"CVE allowlists are managed in Harbor (project ghcr-proxy), never in scripts."
	if runID != "" {
		body += fmt.Sprintf(" Full report: s3://argo-workflows-artifacts/e2e/%s/scan/reports/%s",
			runID, reportFileName(imageSpec{Name: r.Name, Tag: r.Tag}))
	}
	return &junitFailure{
		Message: fmt.Sprintf("%d critical CVE(s) exceed the threshold %d", r.Critical, threshold),
		Body:    body,
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
