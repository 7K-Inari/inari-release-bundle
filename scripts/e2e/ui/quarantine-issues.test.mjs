// Unit tests for the pure decision logic in quarantine-issues.mjs
// (node --test). ReportPortal/GitHub side effects are injected as data and
// not covered here. Fixtures mirror the live-verified RP item shape
// (pathNames.itemPaths, uniqueId launch-specific, Ginkgo label list in the
// item name).

import assert from "node:assert/strict";
import { describe, it } from "node:test";

import {
  BOT_AUTHOR,
  ESCALATION_MARKER,
  MIN_FLAKY_RUNS,
  WARN_MARKER,
  collectCandidates,
  indexLaunchItems,
  issueTitle,
  lastHumanActivity,
  planExpiryActions,
  planIssueActions,
  renderBudgetIssue,
} from "./quarantine-issues.mjs";

const NOW = new Date("2026-11-07T00:00:00Z");
const daysAgo = (n) => new Date(NOW.getTime() - n * 86400_000).toISOString();

let uid = 0;
// rpItem mirrors the live item shape: name, status, type STEP, pathNames.
const rpItem = (path, name, status) => ({
  id: ++uid,
  name,
  status,
  type: "STEP",
  uniqueId: `auto:${Math.random().toString(16).slice(2)}`, // launch-specific
  pathNames: { itemPaths: path.split(" / ").map((n) => ({ id: ++uid, name: n })) },
});
const launch = (id, startTime, items) => ({
  id,
  name: `nightly-${id}`,
  startTime,
  attributes: [{ key: "mode", value: "nightly" }],
  items,
});

const PW_PATH = "specs/rbac/role-lifecycle.spec.ts";
const PW_NAME = "RBAC role lifecycle and propagation @p0 @rbac › create a team";
const GINKGO_PATH = "Golden-Path Suite";
const GINKGO_NAME = "[It] creates a tenant [p0, tenants]";

describe("indexLaunchItems", () => {
  it("excludes the synthetic run-outcome and upgrade anchors", () => {
    const items = [
      rpItem("e2e.run", "workflow-outcome", "FAILED"),
      rpItem("upgrade", "fixture-survives-upgrade", "FAILED"),
      rpItem(PW_PATH, PW_NAME, "FAILED"),
    ];
    const out = indexLaunchItems(items);
    assert.equal(out.size, 1);
    assert.equal([...out.values()][0].failed, 1);
  });

  it("excludes quarantined tests (Playwright tag and Ginkgo label list)", () => {
    const items = [
      rpItem(PW_PATH, "known flaky @p1 @quarantine", "FAILED"),
      rpItem(GINKGO_PATH, "[It] wobbles [p1, tenants, quarantine]", "FAILED"),
      rpItem(PW_PATH, PW_NAME, "FAILED"),
    ];
    assert.equal(indexLaunchItems(items).size, 1);
  });

  it("merges shard duplicates of the same test within a launch", () => {
    // The Playwright setup project runs in BOTH shards (verified live).
    const items = [
      rpItem("fixtures/auth.setup.ts", "authenticate dev-admin", "PASSED"),
      rpItem("fixtures/auth.setup.ts", "authenticate dev-admin", "PASSED"),
    ];
    const out = indexLaunchItems(items);
    assert.equal(out.size, 1);
    assert.equal([...out.values()][0].passed, 2);
  });
});

describe("collectCandidates", () => {
  it("flags a flake (mixed FAILED+PASSED) in 2+ nightly launches", () => {
    const launches = [
      launch(1, daysAgo(1), [rpItem(PW_PATH, PW_NAME, "FAILED"), rpItem(PW_PATH, PW_NAME, "PASSED")]),
      launch(2, daysAgo(3), [rpItem(PW_PATH, PW_NAME, "FAILED"), rpItem(PW_PATH, PW_NAME, "PASSED")]),
    ];
    const out = collectCandidates(launches, { now: NOW });
    assert.equal(out.length, 1);
    assert.equal(out[0].kind, "flake");
    assert.equal(out[0].count, 2);
    assert.equal(out[0].file, PW_PATH);
  });

  it("flags a recurring failure (all-FAILED) with a failure kind", () => {
    const launches = [
      launch(1, daysAgo(1), [rpItem(GINKGO_PATH, GINKGO_NAME, "FAILED")]),
      launch(2, daysAgo(2), [rpItem(GINKGO_PATH, GINKGO_NAME, "FAILED")]),
    ];
    const out = collectCandidates(launches, { now: NOW });
    assert.equal(out.length, 1);
    assert.equal(out[0].kind, "failure");
    assert.equal(issueTitle(out[0]), `[failure] ${GINKGO_PATH} — ${GINKGO_NAME}`);
  });

  it("prefers flake over failure when a launch had a retry pass", () => {
    const launches = [
      launch(1, daysAgo(1), [rpItem(PW_PATH, PW_NAME, "FAILED")]),
      launch(2, daysAgo(2), [rpItem(PW_PATH, PW_NAME, "FAILED"), rpItem(PW_PATH, PW_NAME, "PASSED")]),
    ];
    assert.equal(collectCandidates(launches, { now: NOW })[0].kind, "flake");
  });

  it("ignores tests unstable in only 1 launch", () => {
    const launches = [launch(1, daysAgo(1), [rpItem(PW_PATH, PW_NAME, "FAILED")])];
    assert.deepEqual(collectCandidates(launches, { now: NOW }), []);
  });

  it("counts a launch once even when the test failed in both shards", () => {
    const launches = [
      launch(1, daysAgo(1), [rpItem(PW_PATH, PW_NAME, "FAILED"), rpItem(PW_PATH, PW_NAME, "FAILED")]),
    ];
    assert.deepEqual(collectCandidates(launches, { now: NOW }), []);
  });

  it("ignores launches older than the window", () => {
    const launches = [
      launch(1, daysAgo(1), [rpItem(PW_PATH, PW_NAME, "FAILED")]),
      launch(2, daysAgo(9), [rpItem(PW_PATH, PW_NAME, "FAILED")]),
    ];
    assert.deepEqual(collectCandidates(launches, { now: NOW, windowDays: 7 }), []);
  });

  it("keys candidates by path+name so different tests don't merge", () => {
    const launches = [
      launch(1, daysAgo(1), [rpItem(PW_PATH, "test one @p1 @rbac", "FAILED")]),
      launch(2, daysAgo(2), [rpItem(PW_PATH, "test two @p1 @rbac", "FAILED")]),
    ];
    assert.deepEqual(collectCandidates(launches, { now: NOW }), []);
  });

  it("uses MIN_FLAKY_RUNS = 2 as the threshold", () => {
    assert.equal(MIN_FLAKY_RUNS, 2);
  });
});

describe("planIssueActions", () => {
  const candidate = {
    id: PW_PATH,
    file: PW_PATH,
    title: PW_NAME,
    kind: "flake",
    count: 3,
    runs: [
      { launchId: 1, launchName: "nightly-a", kind: "flake", timestamp: daysAgo(1) },
      { launchId: 2, launchName: "nightly-b", kind: "failure", timestamp: daysAgo(3) },
      { launchId: 3, launchName: "nightly-c", kind: "flake", timestamp: daysAgo(5) },
    ],
  };

  it("creates a new issue when no open issue covers the test", () => {
    const actions = planIssueActions([candidate], []);
    assert.equal(actions.length, 1);
    assert.equal(actions[0].action, "create");
    assert.equal(actions[0].title, issueTitle(candidate));
    assert.ok(actions[0].title.includes(PW_PATH));
    assert.ok(actions[0].body.includes("launches/all/1")); // RP links, not GHA runs
  });

  it("comments on the existing open issue instead of opening a duplicate", () => {
    const issues = [{ number: 42, title: `[flake] ${PW_PATH} — old title`, state: "open" }];
    const actions = planIssueActions([candidate], issues);
    assert.equal(actions[0].action, "comment");
    assert.equal(actions[0].issueNumber, 42);
  });

  it("creates a new issue when the matching issue is closed", () => {
    const issues = [{ number: 42, title: `[flake] ${PW_PATH} — old title`, state: "closed" }];
    assert.equal(planIssueActions([candidate], issues)[0].action, "create");
  });

  it("dedupes Ginkgo candidates on the spec name (shared suite path)", () => {
    const ginkgo = { ...candidate, id: GINKGO_NAME, file: GINKGO_PATH, title: GINKGO_NAME };
    const issues = [{ number: 7, title: `[failure] ${GINKGO_PATH} — ${GINKGO_NAME}`, state: "open" }];
    const actions = planIssueActions([ginkgo], issues);
    assert.equal(actions[0].action, "comment");
    assert.equal(actions[0].issueNumber, 7);
  });
});

describe("lastHumanActivity", () => {
  it("uses the issue creation date when there are no comments", () => {
    const issue = { createdAt: daysAgo(20), comments: [] };
    assert.equal(lastHumanActivity(issue), Date.parse(daysAgo(20)));
  });

  it("ignores bot comments and sweep markers", () => {
    const issue = {
      createdAt: daysAgo(20),
      comments: [
        { author: BOT_AUTHOR, createdAt: daysAgo(1), body: "Flaky again — now 3 launches" },
        { author: "alice", createdAt: daysAgo(2), body: `${WARN_MARKER}\nsome human quoting the marker` },
      ],
    };
    assert.equal(lastHumanActivity(issue), Date.parse(daysAgo(20)));
  });

  it("a human comment resets the clock", () => {
    const issue = {
      createdAt: daysAgo(20),
      comments: [{ author: "alice", createdAt: daysAgo(3), body: "looking into it" }],
    };
    assert.equal(lastHumanActivity(issue), Date.parse(daysAgo(3)));
  });
});

describe("planExpiryActions", () => {
  const openIssue = (createdDaysAgo, comments = []) => ({
    number: 10,
    title: "[flake] x — y",
    state: "open",
    createdAt: daysAgo(createdDaysAgo),
    comments,
  });
  const botComment = (daysAgoN, body) => ({ author: BOT_AUTHOR, createdAt: daysAgo(daysAgoN), body });

  it("warns once at 14 days of human silence", () => {
    const actions = planExpiryActions([openIssue(15)], { now: NOW });
    assert.equal(actions.length, 1);
    assert.equal(actions[0].action, "warn");
    assert.ok(actions[0].body.includes(WARN_MARKER));
  });

  it("does not warn twice for the same human-activity epoch", () => {
    const issue = openIssue(20, [botComment(4, `${WARN_MARKER}\n…`)]);
    assert.deepEqual(planExpiryActions([issue], { now: NOW }), []);
  });

  it("escalates at 30 days and adds the overdue label action", () => {
    const actions = planExpiryActions([openIssue(31, [botComment(10, `${WARN_MARKER}\n…`)])], { now: NOW });
    assert.equal(actions.length, 1);
    assert.equal(actions[0].action, "escalate");
    assert.ok(actions[0].body.includes(ESCALATION_MARKER));
    assert.ok(actions[0].body.includes("quarantine-overdue"));
  });

  it("a human comment resets both warning and escalation", () => {
    const issue = openIssue(40, [
      botComment(20, `${WARN_MARKER}\n…`),
      botComment(9, `${ESCALATION_MARKER}\n…`),
      { author: "alice", createdAt: daysAgo(5), body: "fix in progress" },
    ]);
    assert.deepEqual(planExpiryActions([issue], { now: NOW }), []);
  });

  it("re-warns when the issue goes quiet again after a human comment", () => {
    const issue = openIssue(40, [
      botComment(25, `${WARN_MARKER}\n…`),
      { author: "alice", createdAt: daysAgo(16), body: "fix in progress" },
    ]);
    const actions = planExpiryActions([issue], { now: NOW });
    assert.equal(actions.length, 1);
    assert.equal(actions[0].action, "warn");
  });

  it("ignores closed issues", () => {
    const issue = { ...openIssue(40), state: "closed" };
    assert.deepEqual(planExpiryActions([issue], { now: NOW }), []);
  });
});

describe("renderBudgetIssue", () => {
  it("renders counts, budget status, and the per-test list", () => {
    const r = renderBudgetIssue({
      counts: {
        playwright: 3,
        ginkgo: 1,
        tests: [{ suite: "playwright", location: "specs/a.spec.ts:10", text: "test(… @quarantine …)" }],
      },
      budget: 10,
      now: NOW,
    });
    assert.equal(r.total, 4);
    assert.equal(r.over, false);
    assert.ok(r.body.includes("4 / budget 10"));
    assert.ok(r.body.includes("specs/a.spec.ts:10"));
  });

  it("flags over-budget", () => {
    const r = renderBudgetIssue({ counts: { playwright: 9, ginkgo: 2, tests: [] }, budget: 10, now: NOW });
    assert.equal(r.over, true);
    assert.ok(r.body.includes("OVER BUDGET"));
  });
});
