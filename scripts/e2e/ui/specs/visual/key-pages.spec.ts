// @p2 @visual — container-locked live-environment screenshot baselines.
//
// Dynamic values are masked, never route-stubbed: this remains the real
// console/API/OIDC stack. Baselines must be generated and reviewed in the same
// mcr.microsoft.com/playwright image used by the e2e-stack workflow (see the
// visual/a11y section in scripts/e2e/ui/README.md).

import { expect, test } from "../../fixtures/failure-dump";
import { openKeyPage, VISUAL_PAGES } from "../../fixtures/key-pages";

for (const keyPage of VISUAL_PAGES) {
  test(`${keyPage.name} matches its visual baseline @p2 @visual`, async ({ page }) => {
    await openKeyPage(page, keyPage);
    await expect(keyPage.marker(page)).toBeVisible();
    await expect(page.locator("html")).toHaveAttribute("data-theme", "dark");

    await expect(page).toHaveScreenshot(`${keyPage.name}.png`, {
      fullPage: false,
      clip: keyPage.clip?.(page),
      mask: keyPage.masks?.(page) ?? [],
    });
  });
}
