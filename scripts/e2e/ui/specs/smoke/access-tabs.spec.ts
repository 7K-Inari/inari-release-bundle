// @p0 @smoke — port of the former scripts/e2e/ui-smoke.mjs.
//
// Drives a real browser (authenticated via the setup project's dev-admin
// storageState) through the console boot, the "E2E Org" tenant card, and the
// Access section's four RBAC tabs, asserting each tab's marker text renders.
// The Roles tab / "Manage roles" matrix exist only in the role-engine console
// (inari-ui #85), so this doubles as the regression guard for the
// stale-bundle incident of 2026-10-01.

import { expect, test } from "../../fixtures/failure-dump";

import { TENANT_NAME } from "../../helpers/env";
import { AccessPage } from "../../pages/access/access.page";
import { IdentityTabPage } from "../../pages/access/identity.page";
import { MembersTabPage } from "../../pages/access/members.page";
import { RolesTabPage } from "../../pages/access/roles.page";
import { TeamsRolesTabPage } from "../../pages/access/teams-roles.page";
import { AppShellPage } from "../../pages/app-shell.page";

test("access tabs render their markers @p0 @smoke", async ({ page }) => {
  const shell = new AppShellPage(page);

  // Console boot (storageState skips the KC round-trip; the SPA still gates
  // on "Loading…" while it fetches /me/*).
  await page.goto("/", { waitUntil: "domcontentloaded" });
  await shell.waitForBoot();

  // Tenant switcher home → tenant card → sidebar → Access.
  await shell.openTenant(TENANT_NAME);
  const access = new AccessPage(page);
  await access.goto();

  const tabs = [
    { label: MembersTabPage.LABEL, marker: () => new MembersTabPage(page).marker() },
    { label: TeamsRolesTabPage.LABEL, marker: () => new TeamsRolesTabPage(page).marker() },
    { label: RolesTabPage.LABEL, marker: () => new RolesTabPage(page).marker() },
    { label: IdentityTabPage.LABEL, marker: () => new IdentityTabPage(page).marker() },
  ];

  for (const { label, marker } of tabs) {
    await test.step(`tab "${label}"`, async () => {
      await access.selectTab(label);
      await expect(marker()).toBeVisible();
    });
  }
});
