// Unit tests for the pure decision logic in report-flakes.mjs (node --test).
// The Playwright JSON report fixtures below cover the three statuses the
// reporter must distinguish: flaky (passed after retry), failed, passed.

import assert from "node:assert/strict";
import { describe, it } from "node:test";

import {
  collectFlakes,
  runMetadata,
  buildFlakeReport,
} from "./report-flakes.mjs";

const FIXTURE = {
  suites: [
    {
      title: "smoke/access-tabs.spec.ts",
      suites: [],
      specs: [
        {
          title: "flaky spec passes on retry",
          file: "specs/smoke/access-tabs.spec.ts",
          line: 10,
          tests: [
            {
              status: "flaky",
              results: [{ status: "failed" }, { status: "passed" }],
            },
          ],
        },
        {
          title: "failed spec",
          file: "specs/smoke/access-tabs.spec.ts",
          line: 20,
          tests: [
            {
              status: "unexpected",
              results: [{ status: "failed" }, { status: "failed" }],
            },
          ],
        },
        {
          title: "passed spec",
          file: "specs/smoke/access-tabs.spec.ts",
          line: 30,
          tests: [{ status: "expected", results: [{ status: "passed" }] }],
        },
      ],
    },
    {
      // Nested suite level (describe blocks) — must be walked recursively.
      title: "rbac",
      suites: [
        {
          title: "nested",
          specs: [
            {
              title: "nested flaky spec",
              file: "specs/rbac/role-lifecycle.spec.ts",
              line: 42,
              tests: [
                {
                  status: "flaky",
                  results: [{ status: "timedOut" }, { status: "passed" }],
                },
              ],
            },
          ],
        },
      ],
      specs: [],
    },
  ],
};

describe("collectFlakes", () => {
  it("records only flaky tests (passed after retry), not failed/passed", () => {
    const flakes = collectFlakes(FIXTURE);
    assert.equal(flakes.length, 2);
    const titles = flakes.map((f) => f.title);
    assert.ok(titles.includes("flaky spec passes on retry"));
    assert.ok(titles.includes("nested flaky spec"));
    assert.ok(!titles.includes("failed spec"));
    assert.ok(!titles.includes("passed spec"));
  });

  it("captures file, line, and retry count (attempts minus final pass)", () => {
    const flakes = collectFlakes(FIXTURE);
    const f = flakes.find((x) => x.title === "flaky spec passes on retry");
    assert.equal(f.file, "specs/smoke/access-tabs.spec.ts");
    assert.equal(f.line, 10);
    assert.equal(f.retries, 1);
  });

  it("returns an empty list for a report with no flaky tests", () => {
    assert.deepEqual(collectFlakes({ suites: [] }), []);
  });
});

describe("runMetadata", () => {
  it("maps GitHub Actions env into run metadata", () => {
    const meta = runMetadata({
      GITHUB_RUN_ID: "12345",
      GITHUB_RUN_ATTEMPT: "2",
      GITHUB_REF_NAME: "release-please--main",
      GITHUB_SHA: "abc123",
      GITHUB_REPOSITORY: "7K-Inari/inari-release-bundle",
    });
    assert.equal(meta.runId, "12345");
    assert.equal(meta.runAttempt, "2");
    assert.equal(meta.branch, "release-please--main");
    assert.equal(meta.sha, "abc123");
    assert.equal(
      meta.runUrl,
      "https://github.com/7K-Inari/inari-release-bundle/actions/runs/12345",
    );
  });

  it("falls back to local placeholders outside CI", () => {
    const meta = runMetadata({});
    assert.equal(meta.runId, "local");
    assert.equal(meta.branch, "local");
    assert.equal(meta.runUrl, "");
  });
});

describe("buildFlakeReport", () => {
  it("composes metadata, timestamp, and flakes into the artifact shape", () => {
    const report = buildFlakeReport(FIXTURE, {
      GITHUB_RUN_ID: "999",
      GITHUB_REF_NAME: "main",
      GITHUB_REPOSITORY: "7K-Inari/inari-release-bundle",
    });
    assert.equal(report.runId, "999");
    assert.equal(report.branch, "main");
    assert.ok(Number.isFinite(Date.parse(report.timestamp)));
    assert.equal(report.flakes.length, 2);
    assert.equal(report.flakes[0].file.includes("specs/"), true);
  });
});
