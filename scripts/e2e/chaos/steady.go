package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// SteadyResult is one suite's independent verdict and captured output.
type SteadyResult struct {
	Suite  string
	Phase  string
	Output string
	Err    error
}

// SteadyHandle synchronizes one suite without losing an early process result.
type SteadyHandle struct {
	Suite  string
	Marker string
	Done   <-chan SteadyResult
	Cancel context.CancelFunc
	result *SteadyResult
}

func (h *SteadyHandle) WaitMarker(ctx context.Context) error {
	deadline := time.Now().Add(4 * time.Minute)
	for time.Now().Before(deadline) {
		if info, err := os.Stat(h.Marker); err == nil && info.Size() > 0 {
			select {
			case result := <-h.Done:
				h.result = &result
				return fmt.Errorf("%s completed before both suites were ready; a later run would not be during-fault evidence: %v\n%s", h.Suite, result.Err, result.Output)
			default:
				return nil
			}
		}
		select {
		case result := <-h.Done:
			h.result = &result
			return fmt.Errorf("%s exited before the first real test/spec marker: %v\n%s", h.Suite, result.Err, result.Output)
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
	return fmt.Errorf("%s did not write first real test/spec marker %s before the deadline", h.Suite, h.Marker)
}

func (h *SteadyHandle) Wait() SteadyResult {
	if h.result != nil {
		return *h.result
	}
	result := <-h.Done
	h.result = &result
	return result
}

// Steady coordinates the two required suites. During-fault processes start
// concurrently; injection waits for BOTH first-real-spec/test markers.
type Steady struct {
	Ginkgo     *Ginkgo
	Playwright *Playwright
}

func (s *Steady) Prepare(ctx context.Context) error {
	if s.Ginkgo == nil || s.Playwright == nil {
		return fmt.Errorf("both Ginkgo and Playwright are required")
	}
	if s.Ginkgo.Binary == "" {
		return fmt.Errorf("Ginkgo test binary path is empty")
	}
	if info, err := os.Stat(s.Ginkgo.Binary); err != nil || info.Mode()&0o111 == 0 {
		return fmt.Errorf("Ginkgo test binary %s is missing or not executable: %v", s.Ginkgo.Binary, err)
	}
	return s.Playwright.Prepare(ctx)
}

// Start begins both suites with the same scenario/phase.
func (s *Steady) Start(ctx context.Context, scenario Scenario, phase string) map[string]*SteadyHandle {
	handles := make(map[string]*SteadyHandle, 2)
	for _, item := range []struct {
		name   string
		marker string
		run    func(context.Context, Scenario, string, string) SteadyResult
	}{
		{name: "ginkgo", marker: filepath.Join(s.Playwright.EvidenceDir, "ginkgo-marker-"+phase+".json"), run: s.Ginkgo.Run},
		{name: "playwright", marker: filepath.Join(s.Playwright.EvidenceDir, "marker-"+phase+".json"), run: s.Playwright.Run},
	} {
		_ = os.Remove(item.marker)
		runCtx, cancel := context.WithCancel(ctx)
		done := make(chan SteadyResult, 1)
		go func() {
			done <- item.run(runCtx, scenario, phase, item.marker)
			close(done)
		}()
		handles[item.name] = &SteadyHandle{Suite: item.name, Marker: item.marker, Done: done, Cancel: cancel}
	}
	return handles
}

func steadySuiteNames(handles map[string]*SteadyHandle) []string {
	names := make([]string, 0, len(handles))
	for name := range handles {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func WaitSteadyMarkers(ctx context.Context, handles map[string]*SteadyHandle) error {
	for _, name := range steadySuiteNames(handles) {
		if err := handles[name].WaitMarker(ctx); err != nil {
			return err
		}
	}
	return nil
}

func CollectSteady(handles map[string]*SteadyHandle, cancel bool) map[string]SteadyResult {
	results := make(map[string]SteadyResult, len(handles))
	if cancel {
		for _, handle := range handles {
			handle.Cancel()
		}
	}
	for _, name := range steadySuiteNames(handles) {
		results[name] = handles[name].Wait()
	}
	return results
}

func (s *Steady) Run(ctx context.Context, scenario Scenario, phase string) map[string]SteadyResult {
	return CollectSteady(s.Start(ctx, scenario, phase), false)
}

func steadyFailed(results map[string]SteadyResult) bool {
	for _, result := range results {
		if result.Err != nil {
			return true
		}
	}
	return false
}

func negativeControlEffective(native, ginkgo, playwright, api bool) bool {
	return native && ginkgo && playwright && api
}

func setEnv(env []string, key, value string) []string {
	prefix := key + "="
	filtered := make([]string, 0, len(env)+1)
	for _, item := range env {
		if !strings.HasPrefix(item, prefix) {
			filtered = append(filtered, item)
		}
	}
	return append(filtered, prefix+value)
}

// Ginkgo runs the precompiled attach-mode stack suite. E2E_HA=0 is scoped to
// this process only: the workflow's HA topology remains intact for targeting,
// while the suite's own disruption specs are suppressed.
type Ginkgo struct {
	Runner      CommandRunner
	Binary      string
	EvidenceDir string
	Env         []string
}

func ginkgoFilter(scenario Scenario, phase string) string {
	if phase == "baseline" || phase == "recovery" || scenario.SmokeOnly {
		return "smoke"
	}
	return "(p0||smoke)&&!quarantine"
}

func (g *Ginkgo) Run(ctx context.Context, scenario Scenario, phase, marker string) SteadyResult {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	binary := g.Binary
	if binary == "" {
		binary = "/tools/stack.test"
	}
	junit := filepath.Join(g.EvidenceDir, "ginkgo-"+phase+".junit.xml")
	args := []string{
		"-test.v", "-test.timeout=15m",
		"--ginkgo.no-color",
		"--ginkgo.label-filter=" + ginkgoFilter(scenario, phase),
		"--ginkgo.junit-report=" + junit,
	}
	if scenario.Negative && phase != "baseline" && phase != "recovery" {
		args = append(args, "--ginkgo.flake-attempts=1")
	}
	env := setEnv(g.Env, "E2E_HA", "0")
	env = setEnv(env, "E2E_MIGRATIONS_DIR", "")
	if marker != "" {
		env = setEnv(env, ChaosGinkgoMarkerEnv, marker)
	}
	output, err := g.Runner.Run(ctx, "", env, nil, binary, args...)
	_ = os.WriteFile(filepath.Join(g.EvidenceDir, "ginkgo-"+phase+".log"), []byte(output), 0o644)
	return SteadyResult{Suite: "ginkgo", Phase: phase, Output: output, Err: err}
}

// Kept local to the chaos module so the marker name matches the stack suite's
// public hook without importing that e2e-tagged package.
const ChaosGinkgoMarkerEnv = "E2E_CHAOS_GINKGO_MARKER_FILE"
