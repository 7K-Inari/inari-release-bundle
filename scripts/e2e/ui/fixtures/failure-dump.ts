// Shared auto-fixture: on any test failure, dump what actually rendered —
// page URL, body text excerpt, and section markers — to the test output.
// Failure screenshots are viewport-clipped and traces show data arriving
// without re-render; the DOM dump is what distinguishes "wrong page",
// "data never arrived", and "rendered but selector missed" in one look
// (added after the 2026-10-14 kind debugging cycle where the dump revealed
// tenant-switcher drops the per-hop probe never could).
//
// Specs keep importing { test, expect } — from here instead of
// @playwright/test.

import { test as base } from "@playwright/test";

export const test = base.extend<{ _domDump: void }>({
  _domDump: [
    async ({ page }, use, testInfo) => {
      await use();
      if (testInfo.status === testInfo.expectedStatus) return;
      try {
        const html = await page.content();
        const text = await page.evaluate(() =>
          document.body.innerText.slice(0, 2500),
        );
        console.log("[diag] url:", page.url());
        console.log(
          "[diag] teams-section:",
          html.includes("Team membership"),
          "| roles-table:",
          html.includes("Built-in"),
          "| loading:",
          /Loading…/.test(text),
        );
        console.log("[diag] page text:\n", text);
      } catch (e) {
        console.log("[diag] dump failed:", String(e));
      }
    },
    { scope: "test", auto: true },
  ],
});

export { expect } from "@playwright/test";
