// CI overrides for the console UI e2e suite (release-e2e.yaml / Argo
// e2e-stack template). Serial workers + retries, full failure artifacts,
// JUnit + HTML reports.
//
// ReportPortal: the agent reporter is appended ONLY when RP_ENDPOINT and
// RP_API_KEY are set (mounted by the workflow from cluster secrets — never
// workflow parameters). The JUnit output is the reporting contract
// (/artifacts/playwright/junit.xml via the workflow onExit importer);
// ReportPortal streaming is the optional live view on top.

import { defineConfig } from "@playwright/test";

import base from "./playwright.config";

const RP_ENABLED = Boolean(process.env.RP_ENDPOINT && process.env.RP_API_KEY);

const reportPortal = [
  "@reportportal/agent-js-playwright",
  {
    apiKey: process.env.RP_API_KEY,
    endpoint: process.env.RP_ENDPOINT,
    project: process.env.RP_PROJECT || "inari-e2e",
    launch:
      process.env.RP_LAUNCH ||
      `playwright-${process.env.E2E_MODE || "local"}-${process.env.E2E_RUN_ID || "adhoc"}`,
    attributes: [
      { key: "mode", value: process.env.E2E_MODE || "local" },
      { key: "runId", value: process.env.E2E_RUN_ID || "adhoc" },
    ],
    // A quarantined test must never look like a release-blocking failure in
    // the live stream; it stays excluded by the gate's --grep-invert anyway.
    skippedIssue: false,
  },
] as const;

export default defineConfig({
  ...base,
  retries: 2,
  workers: 1,
  forbidOnly: true,
  reporter: [
    ["junit", { outputFile: "test-results/junit.xml" }],
    ["html", { outputFolder: "playwright-report", open: "never" }],
    ...(RP_ENABLED ? [reportPortal as unknown as [string, Record<string, unknown>]] : []),
  ],
  use: {
    ...base.use,
    screenshot: "only-on-failure",
    video: "retain-on-failure",
    trace: "on-first-retry",
  },
});
