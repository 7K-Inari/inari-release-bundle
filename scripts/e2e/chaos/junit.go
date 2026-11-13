package main

import (
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Check is an independent verdict. A native Litmus pass/score never masks a
// failed browser, domain, cleanup, or recovery check.
type Check struct {
	Name     string        `json:"name"`
	Err      string        `json:"error,omitempty"`
	Output   string        `json:"output,omitempty"`
	Duration time.Duration `json:"duration"`
}

// Report is accumulated in memory and persisted on every exit path.
type Report struct {
	Scenario string  `json:"scenario"`
	RunID    string  `json:"runId"`
	Checks   []Check `json:"checks"`
}

func (r *Report) Add(name string, err error, output string) {
	check := Check{Name: name, Output: truncate(output, 16000)}
	if err != nil {
		check.Err = err.Error()
	}
	r.Checks = append(r.Checks, check)
}

func (r *Report) Failed() bool {
	for _, check := range r.Checks {
		if check.Err != "" {
			return true
		}
	}
	return false
}

type junitSuite struct {
	XMLName   xml.Name    `xml:"testsuite"`
	Name      string      `xml:"name,attr"`
	Tests     int         `xml:"tests,attr"`
	Failures  int         `xml:"failures,attr"`
	Errors    int         `xml:"errors,attr"`
	Time      string      `xml:"time,attr"`
	Testcases []junitCase `xml:"testcase"`
}

type junitCase struct {
	Classname string        `xml:"classname,attr"`
	Name      string        `xml:"name,attr"`
	Time      string        `xml:"time,attr"`
	Failure   *junitFailure `xml:"failure,omitempty"`
	SystemOut string        `xml:"system-out"`
}

type junitFailure struct {
	Message string `xml:"message,attr"`
	Type    string `xml:"type,attr"`
	Body    string `xml:",chardata"`
}

func seconds(duration time.Duration) string {
	if duration < 0 {
		duration = 0
	}
	return strconv.FormatFloat(duration.Seconds(), 'f', 3, 64)
}

// WriteJUnit always emits concrete testcases with numeric time attributes;
// ReportPortal's importer rejects skipped/empty-time synthetic entries.
func (r *Report) WriteJUnit(path string) error {
	if len(r.Checks) == 0 {
		r.Add("driver outcome", errors.New("driver produced no checks"), "missing checks is a driver failure")
	}
	suite := junitSuite{
		Name:  "chaos." + r.Scenario,
		Tests: len(r.Checks),
	}
	var total time.Duration
	for _, check := range r.Checks {
		total += check.Duration
		c := junitCase{
			Classname: "chaos." + r.Scenario,
			Name:      check.Name,
			Time:      seconds(check.Duration),
			SystemOut: check.Output,
		}
		if check.Err != "" {
			suite.Failures++
			c.Failure = &junitFailure{Message: check.Err, Type: "failure", Body: check.Output}
		}
		suite.Testcases = append(suite.Testcases, c)
	}
	suite.Time = seconds(total)
	raw, err := xml.MarshalIndent(suite, "", "  ")
	if err != nil {
		return err
	}
	raw = append([]byte(xml.Header), raw...)
	raw = append(raw, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, raw, 0o644)
}

func (r *Report) WriteEvidence(path string) error {
	raw, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(raw, '\n'), 0o644)
}

func truncate(value string, limit int) string {
	value = strings.TrimSpace(value)
	if len(value) <= limit {
		return value
	}
	return value[:limit] + fmt.Sprintf("\n… truncated %d bytes", len(value)-limit)
}
