#!/usr/bin/env node
// Flake reporter for the console UI e2e suite.
//
// Post-processes the Playwright JSON report (test-results/report.json —
// produced by the JSON reporter in playwright.config.ci.ts) after a CI run.
// Any test Playwright marks "flaky" (passed only after a retry) is recorded:
// a test that needed a retry is by definition a flake candidate, and a green
// gate would otherwise hide it.
//
// Outputs:
//   1. A human summary in the job log (stdout) — one line per flake plus
//      GitHub Actions ::notice annotations.
//   2. A machine-readable artifact test-results/flake-report.json:
//        { runId, runAttempt, runUrl, branch, sha, timestamp,
//          flakes: [{ title, file, line, retries }] }
//      uploaded by the workflow (gate: ui-e2e-flakes-ha-<bool>; nightly:
//      ui-e2e-flakes-*). quarantine-issues.mjs aggregates these artifacts.
//
// Exit code: ALWAYS 0 once the input report was parsed — reporting must never
// fail the gate. Non-zero only when the input report is unreadable/malformed.
//
// Usage: node report-flakes.mjs [path/to/report.json] [path/to/flake-report.json]
//
// Pure decision logic is exported for node:test (report-flakes.test.mjs);
// the side-effecting main() only runs when invoked directly.

import { readFileSync, writeFileSync } from "node:fs";
import { pathToFileURL } from "node:url";

const DEFAULT_INPUT = "test-results/report.json";
const DEFAULT_OUTPUT = "test-results/flake-report.json";

// collectFlakes walks the report's suite tree and returns one entry per
// test Playwright marked "flaky" (any retry that eventually passed).
export function collectFlakes(report) {
  const flakes = [];
  const walk = (suite) => {
    for (const spec of suite.specs ?? []) {
      for (const test of spec.tests ?? []) {
        if (test.status !== "flaky") continue;
        const results = test.results ?? [];
        flakes.push({
          title: spec.title,
          file: spec.file ?? "",
          line: spec.line ?? 0,
          // Attempts before the final passing one.
          retries: Math.max(results.length - 1, 0),
        });
      }
    }
    for (const child of suite.suites ?? []) walk(child);
  };
  for (const suite of report.suites ?? []) walk(suite);
  return flakes;
}

// runMetadata maps the GitHub Actions env into run identity. Outside CI the
// placeholders are "local" so the script is safe to run by hand.
export function runMetadata(env) {
  const runId = env.GITHUB_RUN_ID || "local";
  const repo = env.GITHUB_REPOSITORY || "";
  return {
    runId,
    runAttempt: env.GITHUB_RUN_ATTEMPT || "1",
    branch: env.GITHUB_REF_NAME || "local",
    sha: env.GITHUB_SHA || "",
    runUrl: repo && runId !== "local"
      ? `https://github.com/${repo}/actions/runs/${runId}`
      : "",
  };
}

export function buildFlakeReport(report, env, now = new Date()) {
  return {
    ...runMetadata(env),
    timestamp: now.toISOString(),
    flakes: collectFlakes(report),
  };
}

function printSummary(report) {
  if (report.flakes.length === 0) {
    console.log("flake report: no flaky tests this run");
    return;
  }
  console.log(`flake report: ${report.flakes.length} flaky test(s) this run`);
  for (const f of report.flakes) {
    console.log(`  ${f.file}:${f.line} — ${f.title} (retries: ${f.retries})`);
    // Surface in the Actions UI without failing the step.
    console.log(`::notice title=e2e flake::${f.file}:${f.line} ${f.title} passed after ${f.retries} retr(y/ies)`);
  }
}

async function main() {
  const input = process.argv[2] || DEFAULT_INPUT;
  const output = process.argv[3] || DEFAULT_OUTPUT;

  let report;
  try {
    report = JSON.parse(readFileSync(input, "utf8"));
  } catch (err) {
    // The step is documented as "never fails": a missing report means the
    // Playwright run itself never happened (an earlier step failed and the
    // UI e2e was skipped) — that failure is already surfaced by the step
    // that caused it. Warn and exit 0 instead of masking it with a second,
    // misleading failure.
    console.warn(`report-flakes: no report at ${input} (UI e2e did not run?) — skipping: ${err.message}`);
    process.exit(0);
  }

  const flakeReport = buildFlakeReport(report, process.env);
  printSummary(flakeReport);
  writeFileSync(output, JSON.stringify(flakeReport, null, 2) + "\n");
  console.log(`flake report written to ${output}`);
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  main().catch((err) => {
    console.error(`report-flakes: ${err.message}`);
    process.exit(1);
  });
}
