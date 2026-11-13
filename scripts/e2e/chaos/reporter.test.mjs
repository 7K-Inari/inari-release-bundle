import assert from "node:assert/strict";
import { mkdtempSync, readFileSync, existsSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";

import ChaosMarkerReporter from "./reporter.mjs";

const realTest = (title = "access tabs") => ({ title, project: () => ({ name: "chromium" }) });

test("writes once at the first real test, after setup", () => {
  const dir = mkdtempSync(join(tmpdir(), "chaos-reporter-"));
  const marker = join(dir, "nested", "marker.json");
  const reporter = new ChaosMarkerReporter({ markerFile: marker });
  reporter.onTestBegin({ title: "authenticate", project: () => ({ name: "setup" }) });
  assert.equal(existsSync(marker), false);
  reporter.onTestBegin(realTest());
  reporter.onTestBegin(realTest("second"));
  const data = JSON.parse(readFileSync(marker, "utf8"));
  assert.equal(data.test, "access tabs");
  assert.equal(data.project, "chromium");
});

test("an empty marker path is inert", () => {
  const reporter = new ChaosMarkerReporter({ markerFile: "" });
  assert.doesNotThrow(() => reporter.onTestBegin(realTest()));
});
