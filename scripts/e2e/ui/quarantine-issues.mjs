#!/usr/bin/env node
// Quarantine policy automation for the e2e suites — runs in the e2e-nightly
// workflow ONLY (never in the release gate; the workflow is NON-BLOCKING by
// construction). Data source: ReportPortal (project inari-e2e), fed by the
// e2e-stack WorkflowTemplate exit-handler's JUnit import — one launch per
// run named "<mode>-<uid>" with a mode attribute; skipped testcases are
// dropped at import, so item statuses are clean pass/fail signal.
// ReportPortal visualizes, GitHub issues act (architecture plan §7).
//
// Three duties:
//   1. SWEEP: a test unstable in 2+ nightly launches within 7 days gets an
//      issue "[flake]|[failure] <path> — <name>" labeled `e2e-flake`.
//      Unstable in a launch = mixed FAILED+PASSED items for the same test
//      (Playwright retries:2 emits one JUnit testcase per attempt — a flake)
//      or all-FAILED (a recurring failure; p1/p2 tests fail nightly without
//      blocking the gate). Dedupe is by test id: an open issue for the same
//      test gets a comment with the new occurrences instead of a duplicate.
//      Excluded: the workflow's synthetic anchors (e2e.run/workflow-outcome,
//      upgrade testsuite) and already-quarantined tests (Playwright
//      "@quarantine" in the title, Ginkgo "[..., quarantine]" label list —
//      both survive into the imported item names).
//   2. EXPIRY (docs/e2e/test-taxonomy.md §4.4): an open e2e-flake issue with
//      no HUMAN activity for 14 days gets one warning comment; at 30 days
//      one escalation comment + the `quarantine-overdue` label. Sweep and
//      bot comments never reset the clock. The sweep never closes issues
//      and never deletes tests — fix/re-scope/delete is a human action.
//   3. BUDGET (§4.5): quarantined-test counts grepped from the specs
//      (Playwright @quarantine tags, Ginkgo LabelQuarantine labels) are
//      maintained in a pinned "[quarantine-budget]" tracking issue; over
//      budget (QUARANTINE_BUDGET, default 10) is flagged in the body plus a
//      comment. Quarantine is a remediation queue, not a graveyard.
//
// Env: GITHUB_REPOSITORY + GH_TOKEN|GITHUB_TOKEN (issues:write) — required.
// REPORTPORTAL_API_KEY — read-only RP user (e2e-sweeper, project role
// CUSTOMER); when missing/unreachable the sweep is skipped with a warning
// and expiry + budget still run (GitHub-side only). REPORTPORTAL_BASE_URL
// (default https://reportportal.7kgroup.org — GHA runners cannot reach
// the in-cluster service DNS), REPORTPORTAL_PROJECT (default inari-e2e),
// QUARANTINE_BUDGET (default 10), DRY_RUN=1 (plan and log, no GitHub
// mutations — for first live validation via workflow_dispatch).
// Every section failure degrades to a ::warning; the script exits 0 so the
// nightly stays non-blocking.
//
// Uses the preinstalled `gh` CLI for GitHub API calls and global fetch for
// ReportPortal. Pure decision logic is exported for node:test
// (quarantine-issues.test.mjs); main() only runs when invoked directly.

import { execFileSync } from "node:child_process";
import { readdirSync, readFileSync } from "node:fs";
import { join } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

export const MIN_FLAKY_RUNS = 2;
export const WINDOW_DAYS = 7;
export const FLAKE_LABEL = "e2e-flake";
export const OVERDUE_LABEL = "quarantine-overdue";
export const BUDGET_LABEL = "quarantine-budget";
export const BUDGET_ISSUE_TITLE = "[quarantine-budget] Quarantined-test count";
export const WARN_AFTER_DAYS = 14;
export const ESCALATE_AFTER_DAYS = 30;
export const WARN_MARKER = "<!-- quarantine-sweep:warn-14d -->";
export const ESCALATION_MARKER = "<!-- quarantine-sweep:escalate-30d -->";
export const BUDGET_MARKER = "<!-- quarantine-sweep:budget -->";
export const BOT_AUTHOR = "github-actions[bot]";
export const DEFAULT_BUDGET = 10;
export const DEFAULT_RP_BASE_URL = "https://reportportal.7kgroup.org";
export const DEFAULT_RP_PROJECT = "inari-e2e";

// Synthetic workflow anchors (run-outcome + upgrade legs) — infra signal,
// not tests; never filed as flakes. Matched against the ROOT path segment.
export const EXCLUDED_ROOTS = ["e2e.run", "upgrade"];

// Quarantined tests: Playwright carries "@quarantine" in the test title,
// Ginkgo carries the label list in the item name ("[It] ... [p1, tenants,
// quarantine]"). Both survive the JUnit import into the RP item name
// (verified live 2026-11-07).
const QUARANTINE_NAME = /@quarantine\b|\[[^\]]*\bquarantine\b/;

// ---------------------------------------------------------------------------
// Pure decision logic (unit-tested)
// ---------------------------------------------------------------------------

// itemPath joins the RP pathNames chain ("specs/rbac/x.spec.ts" for
// Playwright, "Golden-Path Suite" for Ginkgo, the chainsaw dir, "e2e.run"
// for the run-outcome anchor). Returns "" when absent.
export const itemPath = (item) =>
  (item?.pathNames?.itemPaths ?? []).map((p) => p.name).join(" / ");

const rootOf = (path) => path.split(" / ")[0];
const leafOf = (path) => path.split(" / ").at(-1);

// candidateId is the dedupe identity carried into issue titles. File-ish
// paths (Playwright spec, chainsaw dir) are unique per file; the Ginkgo
// suite path ("Golden-Path Suite") is shared by every spec, so the spec
// name is the identity there.
const candidateId = (path, name) => (path.includes("/") ? path : name);

// indexLaunchItems groups a launch's STEP items by stable identity and
// counts pass/fail per test. The RP uniqueId is LAUNCH-SPECIFIC (verified
// live 2026-11-07: the same Playwright test has different uniqueIds in
// different launches), so cross-launch grouping must NOT use it.
// Within one launch the same test can appear twice legitimately — the
// Playwright setup project runs in BOTH shards (same name, same uniqueId).
export function indexLaunchItems(items) {
  const byTest = new Map();
  for (const it of items ?? []) {
    const path = itemPath(it);
    if (!path) continue;
    if (EXCLUDED_ROOTS.includes(rootOf(path))) continue;
    if (QUARANTINE_NAME.test(it.name ?? "")) continue; // already tracked by a human
    const key = `${path}::${it.name}`;
    if (!byTest.has(key)) {
      byTest.set(key, { id: candidateId(path, it.name), file: leafOf(path), title: it.name, passed: 0, failed: 0 });
    }
    const e = byTest.get(key);
    if (it.status === "FAILED") e.failed += 1;
    else if (it.status === "PASSED") e.passed += 1;
    // other statuses (e.g. SKIPPED) carry no signal; skipped testcases are
    // dropped at import anyway (skip-dropped import, PR #147).
  }
  return byTest;
}

// collectCandidates aggregates unstable runs per test across nightly
// launches within the window. A launch where the test had ≥1 FAILED item
// counts once; "flake" (some item PASSED — passed after retry) wins over
// "failure" for the issue title. Launches carry {id, name, startTime,
// items:[...]} (items fetched by the caller).
export function collectCandidates(launches, { now = new Date(), windowDays = WINDOW_DAYS, minRuns = MIN_FLAKY_RUNS } = {}) {
  const cutoff = now.getTime() - windowDays * 86400_000;
  const byTest = new Map();
  for (const l of launches) {
    const ts = Date.parse(l.startTime ?? "");
    if (!Number.isFinite(ts) || ts < cutoff) continue;
    for (const [key, e] of indexLaunchItems(l.items)) {
      if (e.failed === 0) continue; // stable (or never failed) in this launch
      const kind = e.passed > 0 ? "flake" : "failure";
      if (!byTest.has(key)) {
        byTest.set(key, { id: e.id, file: e.file, title: e.title, runs: new Map(), flaky: false });
      }
      const c = byTest.get(key);
      if (kind === "flake") c.flaky = true;
      if (!c.runs.has(l.id)) {
        c.runs.set(l.id, { launchId: l.id, launchName: l.name, kind, timestamp: l.startTime });
      }
    }
  }
  return [...byTest.values()]
    .map((c) => ({
      id: c.id,
      file: c.file,
      title: c.title,
      kind: c.flaky ? "flake" : "failure",
      count: c.runs.size,
      runs: [...c.runs.values()],
    }))
    .filter((c) => c.count >= minRuns)
    .sort((a, b) => b.count - a.count);
}

export const issueTitle = (c) => `[${c.kind ?? "flake"}] ${c.file} — ${c.title}`;

// planIssueActions decides create-vs-comment per candidate. Dedupe: an OPEN
// issue whose title contains the candidate id covers it (title text may
// drift when a test is renamed without moving file).
export function planIssueActions(candidates, existingIssues, ctx = {}) {
  const open = existingIssues.filter((i) => i.state === "open");
  return candidates.map((c) => {
    const match = open.find((i) => i.title.includes(c.id));
    if (match) {
      return { action: "comment", issueNumber: match.number, candidate: c, body: occurrenceComment(c, ctx) };
    }
    return { action: "create", candidate: c, title: issueTitle(c), body: issueBody(c, ctx) };
  });
}

const launchUrl = (r, { rpUiBase = DEFAULT_RP_BASE_URL, project = DEFAULT_RP_PROJECT } = {}) =>
  `${rpUiBase}/ui/#${project}/launches/all/${r.launchId}`;

function occurrenceList(c, ctx) {
  return c.runs
    .map((r) => `- ${r.timestamp} — ${r.kind === "flake" ? "flaky (passed after retry)" : "failed"} in [${r.launchName}](${launchUrl(r, ctx)})`)
    .join("\n");
}

function occurrenceComment(c, ctx) {
  const what = c.kind === "flake" ? "Flaky again" : "Failed again";
  return `${what} — now unstable in ${c.count} nightly launches within the last ${WINDOW_DAYS} days:\n${occurrenceList(c, ctx)}`;
}

function issueBody(c, ctx) {
  const first =
    c.kind === "flake"
      ? `\`${c.file}\` — **${c.title}** passed only after a retry in ${c.count} nightly launches within the last ${WINDOW_DAYS} days:`
      : `\`${c.file}\` — **${c.title}** failed in ${c.count} nightly launches within the last ${WINDOW_DAYS} days (a recurring failure, not a retry-flake):`;
  return [
    first,
    "",
    occurrenceList(c, ctx),
    "",
    "**Quarantine policy** (docs/e2e/test-taxonomy.md §4):",
    "1. Tag the test `quarantine` (Playwright `@quarantine` / Ginkgo `Label(\"quarantine\")`) — the gate never selects it; the nightly still runs it.",
    "2. **Expiry:** 14 days without human activity on this issue ⇒ warning; at 30 days the issue is escalated (`quarantine-overdue`) and the test must be fixed, re-scoped, or deleted. Quarantine is a remediation queue, not a graveyard.",
    "3. Fix the flake, remove the tag, and close this issue once the test has been stable in the nightly for ~2 weeks.",
  ].join("\n");
}

// --- expiry policy ---------------------------------------------------------

// lastHumanActivity returns the timestamp of the latest human signal on the
// issue: its creation or the latest comment NOT authored by the bot and NOT
// carrying a sweep marker. Sweep/bot comments never reset the clock.
export function lastHumanActivity(issue, { botAuthor = BOT_AUTHOR } = {}) {
  let ts = Date.parse(issue.createdAt ?? "") || 0;
  for (const c of issue.comments ?? []) {
    if (c.author === botAuthor) continue;
    if ((c.body ?? "").includes("quarantine-sweep:")) continue;
    const t = Date.parse(c.createdAt ?? "");
    if (Number.isFinite(t) && t > ts) ts = t;
  }
  return ts;
}

// planExpiryActions applies the 14/30-day policy to open e2e-flake issues.
// Markers make each notice fire exactly once per human-activity epoch.
export function planExpiryActions(
  issues,
  { now = new Date(), warnAfterDays = WARN_AFTER_DAYS, escalateAfterDays = ESCALATE_AFTER_DAYS, botAuthor = BOT_AUTHOR } = {},
) {
  const actions = [];
  for (const issue of issues) {
    if (issue.state !== "open") continue;
    const last = lastHumanActivity(issue, { botAuthor });
    const ageDays = Math.floor((now.getTime() - last) / 86400_000);
    const botComments = (issue.comments ?? []).filter((c) => c.author === botAuthor);
    const seenSince = (marker) =>
      botComments.some((c) => (c.body ?? "").includes(marker) && Date.parse(c.createdAt ?? "") >= last);
    if (ageDays >= escalateAfterDays && !seenSince(ESCALATION_MARKER)) {
      actions.push({ action: "escalate", issueNumber: issue.number, ageDays, body: escalationComment(ageDays) });
    } else if (ageDays >= warnAfterDays && !seenSince(WARN_MARKER)) {
      actions.push({ action: "warn", issueNumber: issue.number, ageDays, body: warningComment(ageDays) });
    }
  }
  return actions;
}

function warningComment(ageDays) {
  return [
    WARN_MARKER,
    `⏰ Quarantined ${ageDays} days with no fix activity. Per the expiry policy (docs/e2e/test-taxonomy.md §4.4): at **30 days** this issue is escalated and the test must be fixed, re-scoped, or deleted. Any human comment here resets the clock.`,
  ].join("\n");
}

function escalationComment(ageDays) {
  return [
    ESCALATION_MARKER,
    `🚨 Quarantined ${ageDays} days with no fix activity — over the 30-day limit. **The test must now be fixed, re-scoped, or deleted** (docs/e2e/test-taxonomy.md §4.4). Labeling \`quarantine-overdue\`. Any human comment resets the clock; remove the label when the test is fixed or deleted.`,
  ].join("\n");
}

// --- budget metric ----------------------------------------------------------

// renderBudgetIssue builds the pinned tracking-issue body. counts =
// { playwright, ginkgo, tests: [{suite, location, text}] }.
export function renderBudgetIssue({ counts, budget = DEFAULT_BUDGET, now = new Date(), project = DEFAULT_RP_PROJECT }) {
  const total = counts.playwright + counts.ginkgo;
  const over = total > budget;
  const lines = (counts.tests ?? []).map((t) => `- \`${t.location}\` — ${t.text}`);
  const body = [
    BUDGET_MARKER,
    `**Quarantined-test count: ${total} / budget ${budget}** — ${over ? "🔴 OVER BUDGET" : "🟢 within budget"}`,
    `_Updated ${now.toISOString()} by the nightly quarantine-sweep (docs/e2e/test-taxonomy.md §4.5)._`,
    "",
    "| Suite | Quarantined tests | Counting rule |",
    "|---|---|---|",
    `| Playwright (scripts/e2e/ui/specs) | ${counts.playwright} | \`@quarantine\` tags in spec titles |`,
    `| Ginkgo (scripts/e2e/stack) | ${counts.ginkgo} | \`LabelQuarantine\`/\`Label("quarantine")\` in specs |`,
    "",
    `History lives in ReportPortal ([${project} launches](${DEFAULT_RP_BASE_URL}/ui/#${project}/launches/all)).`,
    ...(lines.length ? ["", "**Currently quarantined:**", ...lines] : ["", "No quarantined tests. 🎉"]),
  ].join("\n");
  return { title: BUDGET_ISSUE_TITLE, body, over, total };
}

// ---------------------------------------------------------------------------
// Side effects — ReportPortal (fetch) and GitHub (gh CLI)
// ---------------------------------------------------------------------------

async function rpGet(baseUrl, project, path, params, apiKey) {
  const url = `${baseUrl}/api/v1/${project}/${path}?${new URLSearchParams(params)}`;
  const res = await fetch(url, {
    headers: { Authorization: `Bearer ${apiKey}` },
    signal: AbortSignal.timeout(30_000),
  });
  if (!res.ok) throw new Error(`ReportPortal ${path} -> HTTP ${res.status}: ${(await res.text()).slice(0, 200)}`);
  return res.json();
}

// listNightlyLaunches filters by the deterministic launch name prefix —
// filter.has.attributes is NOT defined on the launch endpoint (service-api
// 5.15.6, verified live: HTTP 400 "Filter parameter attributes is not
// defined") — then verifies the mode attribute client-side.
async function listNightlyLaunches(baseUrl, project, apiKey, sinceMs) {
  const out = await rpGet(
    baseUrl,
    project,
    "launch",
    {
      "filter.cnt.name": "nightly",
      "filter.gt.startTime": String(sinceMs),
      "page.sort": "startTime,DESC",
      "page.size": "50",
    },
    apiKey,
  );
  return (out.content ?? []).filter((l) =>
    (l.attributes ?? []).some((a) => a.key === "mode" && a.value === "nightly"),
  );
}

async function listLaunchSteps(baseUrl, project, apiKey, launchId) {
  const items = [];
  for (let page = 1; ; page += 1) {
    const out = await rpGet(
      baseUrl,
      project,
      "item",
      {
        "filter.eq.launchId": String(launchId),
        "filter.eq.type": "STEP",
        "page.size": "300",
        "page.page": String(page),
      },
      apiKey,
    );
    const batch = out.content ?? [];
    items.push(...batch);
    if (batch.length === 0 || page >= (out.page?.totalPages ?? 1)) break;
  }
  return items;
}

function gh(args) {
  return execFileSync("gh", args, { encoding: "utf8" });
}

function ghJson(args) {
  return JSON.parse(gh(args));
}

function listFlakeIssues(repo, state) {
  return ghJson([
    "issue", "list", "--repo", repo,
    "--label", FLAKE_LABEL, "--state", state, "--limit", "200",
    "--json", "number,title,state,createdAt",
  ]).map((i) => ({ number: i.number, title: i.title, state: i.state.toLowerCase(), createdAt: i.createdAt }));
}

function listIssueComments(repo, issueNumber) {
  return ghJson(["api", `repos/${repo}/issues/${issueNumber}/comments`, "--paginate"]).map((c) => ({
    author: c.user?.login ?? "",
    createdAt: c.created_at,
    body: c.body ?? "",
  }));
}

function ensureLabels(repo, dryRun) {
  for (const [name, color, description] of [
    [FLAKE_LABEL, "d4c5f9", "Flaky/unstable e2e test (quarantine pipeline)"],
    [OVERDUE_LABEL, "b60205", "Quarantined e2e test past the 30-day fix-or-delete limit"],
    [BUDGET_LABEL, "0e8a16", "Quarantined-test budget tracking"],
  ]) {
    if (dryRun) {
      console.log(`DRY_RUN: would ensure label ${name}`);
      continue;
    }
    // --force: create or update — idempotent.
    gh(["label", "create", name, "--repo", repo, "--color", color, "--description", description, "--force"]);
  }
}

function applyIssueActions(repo, actions, dryRun) {
  for (const a of actions) {
    if (dryRun) {
      console.log(`DRY_RUN: would ${a.action === "comment" ? `comment on #${a.issueNumber}` : `create issue "${a.title}"`} (${a.candidate.id})`);
      continue;
    }
    if (a.action === "comment") {
      gh(["issue", "comment", String(a.issueNumber), "--repo", repo, "--body", a.body]);
      console.log(`commented on #${a.issueNumber} (${a.candidate.id})`);
    } else {
      gh(["issue", "create", "--repo", repo, "--title", a.title, "--label", FLAKE_LABEL, "--body", a.body]);
      console.log(`filed issue: ${a.title}`);
    }
  }
}

function applyExpiryActions(repo, actions, dryRun) {
  for (const a of actions) {
    if (dryRun) {
      console.log(`DRY_RUN: would ${a.action} #${a.issueNumber} (age ${a.ageDays}d)`);
      continue;
    }
    gh(["issue", "comment", String(a.issueNumber), "--repo", repo, "--body", a.body]);
    if (a.action === "escalate") {
      gh(["issue", "edit", String(a.issueNumber), "--repo", repo, "--add-label", OVERDUE_LABEL]);
    }
    console.log(`${a.action}d #${a.issueNumber} (age ${a.ageDays}d)`);
  }
}

// scanQuarantined counts quarantined tests from the specs — the source of
// truth for the budget (ReportPortal only sees tests that ran).
function scanQuarantined(repoRoot) {
  const tests = [];
  let playwright = 0;
  let ginkgo = 0;
  const walk = function* (dir) {
    for (const e of readdirSync(dir, { withFileTypes: true })) {
      const p = join(dir, e.name);
      if (e.isDirectory()) yield* walk(p);
      else yield p;
    }
  };
  const scan = (dir, fileRe, lineRe, suite) => {
    let base;
    try {
      base = join(repoRoot, dir);
      readdirSync(base);
    } catch {
      return; // suite tree absent (partial checkout) — count 0
    }
    for (const file of walk(base)) {
      if (!fileRe.test(file)) continue;
      const rel = file.slice(repoRoot.length + 1);
      readFileSync(file, "utf8")
        .split("\n")
        .forEach((text, i) => {
          if (!lineRe.test(text)) return;
          if (suite === "ginkgo") ginkgo += 1;
          else playwright += 1;
          tests.push({ suite, location: `${rel}:${i + 1}`, text: text.trim().slice(0, 120) });
        });
    }
  };
  scan("scripts/e2e/ui/specs", /\.spec\.ts$/, /@quarantine\b/, "playwright");
  scan("scripts/e2e/stack", /_test\.go$/, /LabelQuarantine\b|Label\("quarantine"\)/, "ginkgo");
  return { playwright, ginkgo, tests };
}

function findBudgetIssue(repo) {
  return ghJson([
    "issue", "list", "--repo", repo,
    "--label", BUDGET_LABEL, "--state", "open", "--limit", "20",
    "--json", "number,title,body",
  ]).find((i) => i.title === BUDGET_ISSUE_TITLE);
}

function applyBudgetIssue(repo, rendered, dryRun) {
  const existing = findBudgetIssue(repo);
  if (dryRun) {
    console.log(
      `DRY_RUN: would ${existing ? `update budget issue #${existing.number}` : "create+pin the budget issue"} (total ${rendered.total}${rendered.over ? " OVER BUDGET" : ""})`,
    );
    return;
  }
  if (!existing) {
    const url = gh(["issue", "create", "--repo", repo, "--title", rendered.title, "--label", BUDGET_LABEL, "--body", rendered.body]).trim();
    const number = url.split("/").pop();
    try {
      gh(["api", `repos/${repo}/issues/${number}/pin`, "-X", "PUT"]);
    } catch {
      console.log(`note: could not pin issue #${number} (pin limit?) — leaving unpinned`);
    }
    console.log(`created budget issue: ${url}`);
    return undefined; // body already carries the over-budget flag — no comment on creation
  }
  // Compare ignoring the "_Updated …_" timestamp line so a quiet night
  // produces no issue churn.
  const normalize = (b) => (b ?? "").replace(/^_Updated .*$/m, "").trim();
  if (normalize(existing.body) !== normalize(rendered.body)) {
    gh(["issue", "edit", String(existing.number), "--repo", repo, "--body", rendered.body]);
    console.log(`updated budget issue #${existing.number} (total ${rendered.total})`);
  } else {
    console.log(`budget issue #${existing.number} unchanged (total ${rendered.total})`);
  }
  return existing.number;
}

// ---------------------------------------------------------------------------

async function main() {
  const repo = process.env.GITHUB_REPOSITORY;
  if (!repo || !(process.env.GITHUB_TOKEN || process.env.GH_TOKEN)) {
    console.error("quarantine-issues: GITHUB_REPOSITORY and GH_TOKEN (or GITHUB_TOKEN) are required");
    process.exit(1);
  }
  const dryRun = process.env.DRY_RUN === "1";
  const baseUrl = process.env.REPORTPORTAL_BASE_URL || DEFAULT_RP_BASE_URL;
  const project = process.env.REPORTPORTAL_PROJECT || DEFAULT_RP_PROJECT;
  const apiKey = process.env.REPORTPORTAL_API_KEY || "";
  const budget = Number.parseInt(process.env.QUARANTINE_BUDGET ?? "", 10) || DEFAULT_BUDGET;
  if (dryRun) console.log("DRY_RUN=1 — planning only, no GitHub mutations");

  // Section failures degrade to warnings; the nightly is non-blocking.
  const section = async (name, fn) => {
    try {
      await fn();
    } catch (err) {
      console.warn(`::warning::quarantine-sweep ${name} failed: ${err.message}`);
    }
  };

  await section("setup", async () => ensureLabels(repo, dryRun));

  // 1. Sweep: ReportPortal nightly launches -> e2e-flake issues.
  await section("sweep", async () => {
    if (!apiKey) {
      console.warn(
        "::warning::REPORTPORTAL_API_KEY unset (owner action pending — gitops/e2e/README.md §Prerequisites) — skipping the flake/failure sweep; expiry + budget still run",
      );
      return;
    }
    const since = Date.now() - WINDOW_DAYS * 86400_000;
    const launches = await listNightlyLaunches(baseUrl, project, apiKey, since);
    console.log(`scanning ${launches.length} nightly launch(es) since ${new Date(since).toISOString()}`);
    for (const l of launches) {
      l.items = await listLaunchSteps(baseUrl, project, apiKey, l.id);
    }
    const candidates = collectCandidates(launches);
    if (candidates.length === 0) {
      console.log("no test unstable in 2+ nightly launches within the window — nothing to file");
      return;
    }
    const actions = planIssueActions(candidates, listFlakeIssues(repo, "all"), { rpUiBase: baseUrl, project });
    applyIssueActions(repo, actions, dryRun);
  });

  // 2. Expiry: 14/30-day policy on open e2e-flake issues.
  await section("expiry", async () => {
    const issues = listFlakeIssues(repo, "open").map((i) => ({
      ...i,
      comments: listIssueComments(repo, i.number),
    }));
    const actions = planExpiryActions(issues);
    if (actions.length === 0) {
      console.log(`no e2e-flake issue past the ${WARN_AFTER_DAYS}d mark (${issues.length} open)`);
      return;
    }
    applyExpiryActions(repo, actions, dryRun);
  });

  // 3. Budget: pinned tracking issue with the quarantined-test count.
  await section("budget", async () => {
    const repoRoot = fileURLToPath(new URL("../../..", import.meta.url));
    const counts = scanQuarantined(repoRoot);
    const rendered = renderBudgetIssue({ counts, budget, project });
    console.log(`quarantined tests: ${rendered.total} / budget ${budget}${rendered.over ? " — OVER BUDGET" : ""}`);
    const number = applyBudgetIssue(repo, rendered, dryRun);
    if (rendered.over && !dryRun && number) {
      gh([
        "issue", "comment", String(number), "--repo", repo,
        "--body",
        `🔴 Quarantined-test count is ${rendered.total}, over the budget of ${budget}. Quarantine is a remediation queue, not a graveyard — fix or delete quarantined tests before adding new ones (docs/e2e/test-taxonomy.md §4.5).`,
      ]);
    }
  });
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  main().catch((err) => {
    // Last-resort guard: the nightly must stay non-blocking.
    console.warn(`::warning::quarantine-sweep failed: ${err.message}`);
    process.exit(0);
  });
}
