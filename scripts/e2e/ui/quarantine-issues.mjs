#!/usr/bin/env node
// Quarantine policy automation for the console UI e2e suite — runs in the
// e2e-nightly workflow ONLY (never in the release gate).
//
// Policy (documented in scripts/e2e/README.md): a test that is flaky
// (passed after retry) in 2+ nightly runs within a 7-day window gets a
// GitHub issue titled `[flake] <test path> — <test title>` with the
// `e2e-flake` label. Dedupe is by test path: if an open issue already
// covers the path, the new occurrences are added as a comment instead of
// opening a duplicate. A human then tags the test `@quarantine` (excluded
// from the gate, still run nightly) and closes the issue once the test is
// stable again.
//
// Data source: the ui-e2e-flakes-* artifacts uploaded by recent e2e-nightly
// runs (flake-report.json written by report-flakes.mjs). No state outside
// GitHub artifacts + issues.
//
// Env: GH_TOKEN or GITHUB_TOKEN (issues:write), GITHUB_REPOSITORY (owner/repo).
// Uses the preinstalled `gh` CLI for API calls and artifact download
// (gh handles the artifact zip). Pure decision logic is exported for
// node:test (quarantine-issues.test.mjs); main() only runs when invoked
// directly.

import { execFileSync } from "node:child_process";
import { mkdtempSync, readFileSync, readdirSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { pathToFileURL } from "node:url";

export const MIN_FLAKY_RUNS = 2;
export const WINDOW_DAYS = 7;
export const FLAKE_LABEL = "e2e-flake";
export const NIGHTLY_WORKFLOW = "e2e-nightly.yaml";

const key = (f) => `${f.file}${f.title}`;

// aggregateFlakes counts DISTINCT runs in which each test flaked within the
// window and returns candidates at/over MIN_FLAKY_RUNS. Multiple reports per
// run (HA matrix legs) count once per run.
export function aggregateFlakes(reports, { now = new Date(), windowDays = WINDOW_DAYS } = {}) {
  const cutoff = now.getTime() - windowDays * 86400_000;
  const byTest = new Map();
  for (const r of reports) {
    const ts = Date.parse(r.timestamp ?? "");
    if (!Number.isFinite(ts) || ts < cutoff) continue;
    const seen = new Set();
    for (const f of r.flakes ?? []) {
      const k = key(f);
      if (seen.has(k)) continue; // one occurrence per run per test
      seen.add(k);
      if (!byTest.has(k)) byTest.set(k, { file: f.file, title: f.title, runs: new Map() });
      const entry = byTest.get(k);
      if (!entry.runs.has(r.runId)) {
        entry.runs.set(r.runId, { runId: r.runId, runUrl: r.runUrl ?? "", timestamp: r.timestamp });
      }
    }
  }
  return [...byTest.values()]
    .map((e) => ({ file: e.file, title: e.title, count: e.runs.size, runs: [...e.runs.values()] }))
    .filter((e) => e.count >= MIN_FLAKY_RUNS)
    .sort((a, b) => b.count - a.count);
}

export const issueTitle = (c) => `[flake] ${c.file} — ${c.title}`;

// planIssueActions decides create-vs-comment per candidate. Dedupe: an OPEN
// issue whose title contains the test path covers it (title text may drift
// when a test is renamed without moving file).
export function planIssueActions(candidates, existingIssues) {
  const open = existingIssues.filter((i) => i.state === "open");
  return candidates.map((c) => {
    const match = open.find((i) => i.title.includes(c.file));
    if (match) {
      return { action: "comment", issueNumber: match.number, candidate: c, body: occurrenceComment(c) };
    }
    return { action: "create", candidate: c, title: issueTitle(c), body: issueBody(c) };
  });
}

function occurrenceList(c) {
  return c.runs
    .map((r) => `- ${r.timestamp} — ${r.runUrl || `run ${r.runId}`}`)
    .join("\n");
}

function occurrenceComment(c) {
  return `Flaky again — now ${c.count} runs in the last ${WINDOW_DAYS} days:\n${occurrenceList(c)}`;
}

function issueBody(c) {
  return [
    `\`${c.file}\` — **${c.title}** passed only after a retry in ${c.count} nightly runs within the last ${WINDOW_DAYS} days:`,
    "",
    occurrenceList(c),
    "",
    "**Quarantine policy** (see scripts/e2e/README.md):",
    `1. Tag the test \`@quarantine\` — the gate greps \`--grep-invert @quarantine\`, so it stops blocking releases.`,
    "2. The nightly still runs it; watch the `ui-e2e-flakes-*` artifacts.",
    "3. Fix the flake, remove the tag, and close this issue once the test is stable in the nightly.",
  ].join("\n");
}

// --- side effects (gh CLI) ------------------------------------------------

function gh(args) {
  return execFileSync("gh", args, { encoding: "utf8" });
}

function ghJson(args) {
  return JSON.parse(gh(args));
}

function listRecentNightlyRuns(repo, sinceIso) {
  const out = ghJson([
    "api",
    `repos/${repo}/actions/workflows/${NIGHTLY_WORKFLOW}/runs?created=>=${sinceIso}&per_page=100`,
  ]);
  return (out.workflow_runs ?? []).map((r) => r.id);
}

// collectReports downloads the ui-e2e-flakes artifacts of each run
// (`gh run download` unzips for us) and parses every flake-report.json.
function collectReports(repo, runIds) {
  const dir = mkdtempSync(join(tmpdir(), "flake-reports-"));
  const reports = [];
  for (const id of runIds) {
    const dest = join(dir, String(id));
    try {
      gh(["run", "download", String(id), "--repo", repo, "--name", "ui-e2e-flakes", "--dir", dest]);
    } catch {
      // artifact name has a suffix per matrix leg; try glob patterns
      try {
        gh(["run", "download", String(id), "--repo", repo, "--pattern", "ui-e2e-flakes-*", "--dir", dest]);
      } catch {
        continue; // no flake artifacts on this run
      }
    }
    const walk = (d) => {
      for (const e of readdirSync(d, { withFileTypes: true })) {
        const p = join(d, e.name);
        if (e.isDirectory()) walk(p);
        else if (e.name === "flake-report.json") {
          try {
            reports.push(JSON.parse(readFileSync(p, "utf8")));
          } catch {
            // malformed artifact — skip
          }
        }
      }
    };
    walk(dest);
  }
  return reports;
}

function listFlakeIssues(repo) {
  return ghJson([
    "issue", "list", "--repo", repo,
    "--label", FLAKE_LABEL, "--state", "all", "--limit", "200",
    "--json", "number,title,state",
  ]).map((i) => ({ number: i.number, title: i.title, state: i.state.toLowerCase() }));
}

function applyActions(repo, actions) {
  for (const a of actions) {
    if (a.action === "comment") {
      gh(["issue", "comment", String(a.issueNumber), "--repo", repo, "--body", a.body]);
      console.log(`commented on #${a.issueNumber} (${a.candidate.file})`);
    } else {
      gh(["issue", "create", "--repo", repo, "--title", a.title, "--label", FLAKE_LABEL, "--body", a.body]);
      console.log(`filed issue: ${a.title}`);
    }
  }
}

async function main() {
  const repo = process.env.GITHUB_REPOSITORY;
  // gh CLI accepts either; the workflow exports GH_TOKEN.
  if (!repo || !(process.env.GITHUB_TOKEN || process.env.GH_TOKEN)) {
    console.error("quarantine-issues: GITHUB_REPOSITORY and GH_TOKEN (or GITHUB_TOKEN) are required");
    process.exit(1);
  }
  const since = new Date(Date.now() - WINDOW_DAYS * 86400_000).toISOString();
  const runIds = listRecentNightlyRuns(repo, since);
  console.log(`scanning ${runIds.length} nightly run(s) since ${since}`);
  const reports = collectReports(repo, runIds);
  const candidates = aggregateFlakes(reports);
  if (candidates.length === 0) {
    console.log("no test flaked in 2+ runs within the window — nothing to file");
    return;
  }
  const actions = planIssueActions(candidates, listFlakeIssues(repo));
  applyActions(repo, actions);
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  main().catch((err) => {
    console.error(`quarantine-issues: ${err.message}`);
    process.exit(1);
  });
}
