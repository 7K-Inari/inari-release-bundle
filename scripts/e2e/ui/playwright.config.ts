// Local defaults for the console UI e2e suite. CI overrides live in
// playwright.config.ci.ts.
//
// Projects: `setup` authenticates personas through the real Keycloak login
// (fixtures/auth.ts) and stores storageState files under .auth/; test
// projects depend on it and reuse the stored state.

import { defineConfig, devices } from "@playwright/test";

import { UI_BASE } from "./helpers/env";

export const AUTH_DIR = ".auth";

export default defineConfig({
  testDir: ".",
  outputDir: "test-results",
  fullyParallel: false,
  forbidOnly: false,
  retries: 0,
  reporter: "list",
  timeout: 60_000,
  expect: {
    timeout: 20_000,
    // One deterministic screenshot contract for specs/visual. Baselines are
    // linux/chromium-only and generated in the workflow's Playwright image.
    toHaveScreenshot: {
      animations: "disabled",
      caret: "hide",
      maxDiffPixelRatio: 0.001,
      scale: "css",
    },
  },
  use: {
    baseURL: UI_BASE,
    trace: "off",
    screenshot: "off",
    video: "off",
  },
  projects: [
    {
      name: "setup",
      testMatch: /fixtures\/.*\.setup\.ts/,
    },
    {
      name: "chromium",
      use: {
        ...devices["Desktop Chrome"],
        storageState: `${AUTH_DIR}/dev-admin.json`,
      },
      dependencies: ["setup"],
      testMatch: /specs\/.*\.spec\.ts/,
    },
  ],
});
