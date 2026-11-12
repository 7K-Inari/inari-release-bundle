// @p2 @a11y — automated WCAG 2.0/2.1 A+AA scans against the real console.
//
// Fix-forward in inari-ui is the default. A genuinely accepted violation must
// be rule-scoped to ONE key page and carry a tracking issue; stale exclusions
// fail so fixed issues cannot leave permanent blind spots. Selector/subtree
// exclusions and blanket disableRules are intentionally unsupported.

import { expect, test } from "../../fixtures/failure-dump";
import { KEY_PAGES, openKeyPage } from "../../fixtures/key-pages";
import {
  formatViolations,
  partitionViolations,
  scanA11y,
  staleExclusions,
  type AcceptedViolation,
} from "../../helpers/a11y";

const ACCEPTED: Readonly<Record<string, readonly AcceptedViolation[]>> = {};

for (const keyPage of KEY_PAGES) {
  test(`${keyPage.name} has no unexpected WCAG violations @p2 @a11y`, async ({ page }) => {
    const accepted = ACCEPTED[keyPage.name] ?? [];
    await openKeyPage(page, keyPage);
    await expect(keyPage.marker(page)).toBeVisible();

    const violations = await scanA11y(page);
    const { expected, unexpected } = partitionViolations(violations, [...accepted]);
    const stale = staleExclusions(violations, [...accepted]);

    if (expected.length > 0) {
      await test.info().attach(`${keyPage.name}-accepted-a11y-violations.json`, {
        contentType: "application/json",
        body: JSON.stringify(expected, null, 2),
      });
    }

    expect(
      stale.map((entry) => entry.rule),
      "accepted exclusion no longer matches a violation; remove it and close out the tracking issue",
    ).toEqual([]);
    expect(
      formatViolations(unexpected),
      `unexpected accessibility violations on ${keyPage.name}`,
    ).toBe("");
  });
}
