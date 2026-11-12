// Unit tests for the pure parts of helpers/a11y.ts (violation partitioning,
// stale-exclusion detection, failure-message formatting). The scan wrapper
// itself is exercised by the @a11y specs against the live env.

import assert from "node:assert/strict";
import { describe, it } from "node:test";

import {
  formatViolations,
  partitionViolations,
  staleExclusions,
  type ScanViolation,
} from "./a11y.ts";

function violation(id: string, targets: string[][], impact = "serious"): ScanViolation {
  return {
    id,
    impact,
    description: `${id} description`,
    nodes: targets.map((target) => ({ target })),
  };
}

describe("partitionViolations", () => {
  it("puts accepted rules in expected and everything else in unexpected", () => {
    const violations = [violation("color-contrast", [[".a"]]), violation("label", [[".b"]])];
    const accepted = [{ rule: "color-contrast", issue: "7K-Inari/inari-ui#1", reason: "dark theme" }];
    const { expected, unexpected } = partitionViolations(violations, accepted);
    assert.deepEqual(expected.map((v) => v.id), ["color-contrast"]);
    assert.deepEqual(unexpected.map((v) => v.id), ["label"]);
  });

  it("rejects an accepted-exclusion entry without a linked issue", () => {
    assert.throws(
      () =>
        partitionViolations([], [
          { rule: "color-contrast", issue: "", reason: "no issue link" },
        ]),
      /issue/,
    );
  });

  it("rejects an accepted-exclusion entry without a rationale", () => {
    assert.throws(
      () =>
        partitionViolations([], [
          { rule: "color-contrast", issue: "7K-Inari/inari-ui#1", reason: "" },
        ]),
      /reason/,
    );
  });

  it("treats an empty accepted list as all-unexpected", () => {
    const { expected, unexpected } = partitionViolations([violation("label", [[".b"]])], []);
    assert.equal(expected.length, 0);
    assert.equal(unexpected.length, 1);
  });
});

describe("staleExclusions", () => {
  it("flags accepted rules that no longer violate (exclusion can be removed)", () => {
    const accepted = [
      { rule: "color-contrast", issue: "7K-Inari/inari-ui#1", reason: "fixed upstream" },
    ];
    assert.deepEqual(staleExclusions([], accepted), accepted);
    assert.deepEqual(staleExclusions([violation("color-contrast", [[".a"]])], accepted), []);
  });
});

describe("formatViolations", () => {
  it("returns an empty string for no violations", () => {
    assert.equal(formatViolations([]), "");
  });

  it("renders rule id, impact, and capped node targets", () => {
    const out = formatViolations([
      violation("color-contrast", [[".a"], [".b"], [".c"], [".d"]], "critical"),
    ]);
    assert.match(out, /color-contrast/);
    assert.match(out, /critical/);
    assert.match(out, /\.a/);
    assert.match(out, /and 1 more/);
  });
});
