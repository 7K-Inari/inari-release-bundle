package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

func main() {
	os.Exit(realMain(os.Getenv, os.Stdout))
}

func realMain(getenv func(string) string, stdout io.Writer) int {
	outDir, evidenceDir := artifactDirs(getenv)
	if err := makeArtifactDirs(outDir, evidenceDir); err != nil {
		fmt.Fprintf(stdout, "CONFIGURATION ERROR: artifact directories: %v\n", err)
		return 2
	}
	report := &Report{Scenario: scenarioName(getenv("CHAOS_SCENARIO")), RunID: getenv("E2E_RUN_ID")}
	cfg, err := loadConfig(getenv)
	if err != nil {
		report.Add("configuration", err, "validation stopped before any Kubernetes read or mutation")
		return finish(report, outDir, evidenceDir, stdout)
	}
	report.Scenario = cfg.Scenario.Name

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	runner := ExecRunner{Stdout: stdout}
	kube := &KubeClient{Binary: cfg.Kubectl, Namespace: cfg.Namespace, Runner: runner}
	api := NewAPIClient(cfg)
	suiteEnv := []string{
		"CI=1",
		"E2E_RUN_ID=" + cfg.RunID,
		"CHAOS_SCENARIO=" + cfg.Scenario.Name,
	}
	steady := &Steady{
		Ginkgo: &Ginkgo{
			Runner:      runner,
			Binary:      cfg.GinkgoBinary,
			EvidenceDir: cfg.EvidenceDir,
			Env:         append([]string(nil), suiteEnv...),
		},
		Playwright: &Playwright{
			Runner:      runner,
			UIDir:       cfg.UIDir,
			EvidenceDir: cfg.EvidenceDir,
			Env:         append([]string(nil), suiteEnv...),
		},
	}
	execute(ctx, cfg, report, kube, api, steady)
	return finish(report, cfg.OutDir, cfg.EvidenceDir, stdout)
}

func finish(report *Report, outDir, evidenceDir string, stdout io.Writer) int {
	var errs []error
	if err := report.WriteEvidence(filepath.Join(evidenceDir, "driver-report.json")); err != nil {
		errs = append(errs, err)
	}
	if err := report.WriteJUnit(filepath.Join(outDir, "junit.xml")); err != nil {
		errs = append(errs, err)
	}
	if len(errs) > 0 {
		fmt.Fprintf(stdout, "ERROR: writing chaos evidence/JUnit: %v\n", errors.Join(errs...))
		return 2
	}
	if report.Failed() {
		fmt.Fprintf(stdout, "== chaos.%s FAILED; JUnit and evidence archived\n", report.Scenario)
		return 1
	}
	fmt.Fprintf(stdout, "== chaos.%s passed; JUnit and evidence archived\n", report.Scenario)
	return 0
}

func execute(ctx context.Context, cfg Config, report *Report, kube *KubeClient, api *APIClient, steady *Steady) {
	if err := preflight(ctx, kube); err != nil {
		report.Add("Litmus/operator preflight", err, "CRDs are proven Established by GitOps installation; API discovery and operator readiness are rechecked here")
		return
	}
	if err := ensureNoStaleResources(ctx, kube, cfg.RunID); err != nil {
		report.Add("stale-resource preflight", err, "")
		return
	}
	if cfg.Scenario.RequiresCNPG {
		if err := waitCNPGHealthy(ctx, kube, "", false); err != nil {
			report.Add("three-instance CNPG preflight", err, "failover is invalid until all three e2e instances are healthy")
			return
		}
	}
	if err := steady.Prepare(ctx); err != nil {
		report.Add("steady-state dependency setup", err, "Ginkgo binary or Playwright npm setup failed before fault injection")
		return
	}
	baseline := steady.Run(ctx, cfg.Scenario, "baseline")
	for _, suite := range []string{"ginkgo", "playwright"} {
		result := baseline[suite]
		report.Add("baseline "+suite+" @smoke", result.Err, result.Output)
	}
	if steadyFailed(baseline) {
		return
	}

	state := &DomainState{}
	if err := prepareScenario(ctx, cfg, api, kube, state); err != nil {
		report.Add("scenario fixture/precondition", err, "no fault injected")
		_ = state.WriteEvidence(filepath.Join(cfg.EvidenceDir, "domain-probes.json"))
		return
	}

	var engines []*Engine
	negativeNativeFailed := false
	negativeSteadyFailed := map[string]bool{}
	negativeAPIFailed := false

	for _, key := range cfg.Scenario.Engines {
		if key == "openfga-timeout" {
			if err := prepareOpenFGATimeout(ctx, cfg, api, state); err != nil {
				report.Add("OpenFGA fresh tenant/token prevalidation", err, "second fault not injected")
				break
			}
			report.Add("OpenFGA fresh tenant/token prevalidation", nil, "fresh object readable; PEP cache TTL expired before timeout window")
		}

		targets, err := resolveTargets(ctx, cfg, kube, key)
		if err != nil {
			report.Add(key+" target snapshot", err, "empty or invalid targets fail closed; no engine applied")
			break
		}
		engine, err := renderEngine(cfg, key, targets)
		if err != nil {
			report.Add(key+" manifest rendering", err, "")
			break
		}
		if err := engine.WriteManifests(cfg.EvidenceDir); err != nil {
			report.Add(key+" evidence setup", err, "")
			break
		}
		targetEvidence, _ := json.MarshalIndent(map[string]any{
			"engine": engine.Name, "pods": targets, "destinationHosts": engine.Definition.DestinationHosts,
		}, "", "  ")
		_ = os.WriteFile(filepath.Join(cfg.EvidenceDir, "target-"+key+".json"), append(targetEvidence, '\n'), 0o644)
		report.Add(key+" target snapshot", nil, string(targetEvidence))

		// Record intent before apply so even a partially successful API write is
		// followed by exact-name cleanup on the failure path.
		engine.ExperimentApplied = true
		engines = append(engines, engine)
		if err := kube.Apply(ctx, engine.Experiment); err != nil {
			report.Add(key+" experiment definition", err, "")
			break
		}

		var handles map[string]*SteadyHandle
		if engine.Definition.Steady {
			handles = steady.Start(ctx, cfg.Scenario, key)
			if err := WaitSteadyMarkers(ctx, handles); err != nil {
				results := CollectSteady(handles, true)
				output := err.Error()
				for _, suite := range []string{"ginkgo", "playwright"} {
					output += "\n" + suite + ": " + results[suite].Output
				}
				report.Add(key+" Ginkgo+Playwright first-spec/test synchronization", err, output)
				break
			}
			report.Add(key+" Ginkgo+Playwright first-spec/test synchronization", nil, "both real-suite markers observed before engine apply")
		}

		engine.Applied = true // cleanup intent, including partial apply failures
		if err := kube.Apply(ctx, engine.Manifest); err != nil {
			report.Add(key+" engine apply", err, "")
			if handles != nil {
				results := CollectSteady(handles, true)
				for _, suite := range []string{"ginkgo", "playwright"} {
					result := results[suite]
					report.Add(key+" "+suite+" after failed engine apply", result.Err, result.Output)
				}
			}
			break
		}
		report.Add(key+" engine apply", nil, engine.Name)

		runningRaw, runningErr := waitForResult(ctx, kube, engine, false)
		if runningErr != nil {
			report.Add(key+" injection evidence", runningErr, string(runningRaw))
			if handles != nil {
				results := CollectSteady(handles, true)
				for _, suite := range []string{"ginkgo", "playwright"} {
					result := results[suite]
					report.Add(key+" "+suite+" after failed injection", result.Err, result.Output)
				}
			}
			archiveEngine(ctx, kube, engine, cfg.EvidenceDir)
			break
		}
		report.Add(key+" injection evidence", nil, "ChaosResult phase=Running with a nonempty exact target snapshot")

		duringErr := duringEngine(ctx, cfg, api, kube, key, state)
		if cfg.Scenario.Negative {
			negativeAPIFailed = duringErr == nil
			if duringErr == nil {
				report.Add("negative-control API outage observed", nil, "the pre-fault 401 probe no longer succeeds")
			} else {
				report.Add("negative-control API outage observed", duringErr, "")
			}
		} else {
			report.Add(key+" scenario invariant during fault", duringErr, "independent domain assertion")
		}

		if handles != nil {
			results := CollectSteady(handles, false)
			for _, suite := range []string{"ginkgo", "playwright"} {
				result := results[suite]
				if cfg.Scenario.Negative {
					negativeSteadyFailed[suite] = result.Err != nil
					if result.Err != nil {
						report.Add("negative-control "+suite+" steady-state failure observed", nil, result.Output)
					} else {
						report.Add("negative-control "+suite+" steady-state failure observed", fmt.Errorf("%s unexpectedly passed during a full server outage", suite), result.Output)
					}
				} else {
					report.Add(key+" during-fault "+suite, result.Err, result.Output)
				}
			}
		}

		raw, terminalErr := waitForResult(ctx, kube, engine, true)
		engine.RawResult = raw
		archiveEngine(ctx, kube, engine, cfg.EvidenceDir)
		if terminalErr != nil {
			report.Add(key+" native ChaosResult", terminalErr, string(raw))
			break
		}
		result, parseErr := parseChaosResult(raw, engine.ResultName)
		if parseErr != nil {
			report.Add(key+" native ChaosResult", parseErr, string(raw))
			break
		}
		score, scoreErr := result.Score()
		report.Add(key+" resilience score = "+score+"%", scoreErr, "native Litmus probeSuccessPercentage, unchanged")
		if cfg.Scenario.Negative {
			negativeNativeFailed = result.FailedAsExpected()
			if negativeNativeFailed {
				report.Add("negative-control native failure observed", nil, fmt.Sprintf("phase=%s verdict=%s score=%s%%", result.Phase(), result.Verdict(), score))
			} else {
				report.Add("negative-control native failure observed", fmt.Errorf("native phase=%s verdict=%s did not fail", result.Phase(), result.Verdict()), string(raw))
			}
		} else {
			report.Add(key+" native phase/verdict", result.Passed(), fmt.Sprintf("phase=%s verdict=%s probeSuccessPercentage=%s%%", result.Phase(), result.Verdict(), score))
		}

		if report.Failed() {
			break
		}
	}

	// Cleanup has a fresh context: cancellation of the driver must still stop
	// the native fault and remove only the exact run-scoped resources.
	cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cleanupCancel()
	for i := len(engines) - 1; i >= 0; i-- {
		engine := engines[i]
		if err := CleanupEngine(cleanupCtx, kube, engine); err != nil {
			report.Add(engine.Definition.Key+" cleanup", err, "stop/delete attempted for exact recorded names")
			continue
		}
		report.Add(engine.Definition.Key+" cleanup", nil, engine.Name)
		if engine.Applied {
			if err := leftoverResources(cleanupCtx, kube, engine, cfg.RunID); err != nil {
				report.Add(engine.Definition.Key+" leftover check", err, "no broad-label deletion was attempted")
			} else {
				report.Add(engine.Definition.Key+" leftover check", nil, "no run/engine-labelled resources remain")
			}
		}
	}

	recoveryCtx := cleanupCtx
	if ctx.Err() == nil {
		var cancel context.CancelFunc
		recoveryCtx, cancel = context.WithTimeout(context.Background(), 8*time.Minute)
		defer cancel()
	}
	if err := recoverScenario(recoveryCtx, cfg, api, kube, state); err != nil {
		report.Add("domain recovery", err, "")
	} else {
		report.Add("domain recovery", nil, "scenario-specific recovery assertions passed")
	}
	recovery := steady.Run(recoveryCtx, cfg.Scenario, "recovery")
	for _, suite := range []string{"ginkgo", "playwright"} {
		result := recovery[suite]
		report.Add("recovery "+suite+" @smoke", result.Err, result.Output)
	}
	if err := state.WriteEvidence(filepath.Join(cfg.EvidenceDir, "domain-probes.json")); err != nil {
		report.Add("domain evidence", err, "")
	}

	if cfg.Scenario.Negative {
		ginkgoFailed := negativeSteadyFailed["ginkgo"]
		playwrightFailed := negativeSteadyFailed["playwright"]
		switch {
		case negativeControlEffective(negativeNativeFailed, ginkgoFailed, playwrightFailed, negativeAPIFailed):
			// The negative control is effective only when the run is red. Keep a
			// deliberate failing testcase after all cleanup/recovery checks.
			report.Add("negative-control intentional workflow failure", fmt.Errorf("negative control was effective; this manual run must fail by design"), "native probes, API, Ginkgo, and Playwright all observed the outage; cleanup and recovery completed")
		default:
			report.Add("negative-control intentional workflow failure", fmt.Errorf("negative control ineffective (native=%t ginkgo=%t playwright=%t API=%t)", negativeNativeFailed, ginkgoFailed, playwrightFailed, negativeAPIFailed), "the outage did not affect every required probe; do not claim negative-control validation")
		}
	}
}

func archiveEngine(ctx context.Context, kube *KubeClient, engine *Engine, evidenceDir string) {
	for _, dir := range []string{"results", "logs", "engines-live"} {
		if err := os.MkdirAll(filepath.Join(evidenceDir, dir), 0o755); err != nil {
			continue
		}
	}
	if len(engine.RawResult) > 0 {
		_ = os.WriteFile(filepath.Join(evidenceDir, "results", engine.Definition.Key+".json"), engine.RawResult, 0o644)
	}
	if raw, err := kube.GetJSON(ctx, "chaosengine", engine.Name); err == nil {
		_ = os.WriteFile(filepath.Join(evidenceDir, "engines-live", engine.Definition.Key+".json"), raw, 0o644)
	}
	logs, err := kube.Output(ctx, nil, "logs", "-l", "chaosEngineName="+engine.Name, "--all-containers=true", "--prefix=true", "--tail=-1")
	if err == nil {
		_ = os.WriteFile(filepath.Join(evidenceDir, "logs", engine.Definition.Key+".log"), []byte(logs), 0o644)
	}
}
