package main

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"time"
)

type Playwright struct {
	Runner      CommandRunner
	UIDir       string
	EvidenceDir string
	Env         []string
}

func (p *Playwright) Prepare(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Minute)
	defer cancel()
	_, err := p.Runner.Run(ctx, p.UIDir, p.Env, nil, "npm", "ci", "--include=dev", "--no-audit", "--no-fund")
	return err
}

type PlaywrightResult = SteadyResult

func selectionArgs(scenario Scenario, baseline bool) []string {
	if baseline || scenario.SmokeOnly {
		return []string{"--grep", "@smoke", "--grep-invert", "@quarantine"}
	}
	return []string{"--grep", "@p0|@smoke", "--grep-invert", "@quarantine"}
}

func (p *Playwright) Run(ctx context.Context, scenario Scenario, phase string, marker string) PlaywrightResult {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	args := []string{"playwright", "test", "--config", "playwright.config.ci.ts"}
	args = append(args, selectionArgs(scenario, phase == "baseline" || phase == "recovery")...)
	if scenario.Negative && phase != "baseline" && phase != "recovery" {
		// A retry after the 120s outage could hide the required steady-state
		// failure. Positive scenarios retain the normal CI retry contract.
		args = append(args, "--retries=0")
	}
	env := setEnv(p.Env, "PLAYWRIGHT_JUNIT_OUTPUT_FILE", filepath.Join(p.EvidenceDir, "playwright-"+phase+".junit.xml"))
	if marker != "" {
		env = setEnv(env, "E2E_CHAOS_REPORTER", "1")
		env = setEnv(env, "E2E_CHAOS_MARKER_FILE", marker)
	}
	output, err := p.Runner.Run(ctx, p.UIDir, env, nil, "npx", args...)
	for _, report := range []struct{ source, destination string }{
		{source: "playwright-report", destination: "playwright-" + phase + "-html"},
		{source: "test-results", destination: "playwright-" + phase + "-results"},
	} {
		if copyErr := copyDirectory(filepath.Join(p.UIDir, report.source), filepath.Join(p.EvidenceDir, report.destination)); copyErr != nil {
			output += "\nartifact copy " + report.source + ": " + copyErr.Error()
		}
	}
	return SteadyResult{Suite: "playwright", Phase: phase, Output: output, Err: err}
}

func copyDirectory(source, destination string) error {
	if _, err := os.Stat(source); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	return filepath.Walk(source, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		target := filepath.Join(destination, relative)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.Create(target)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(out, in)
		closeErr := out.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	})
}
