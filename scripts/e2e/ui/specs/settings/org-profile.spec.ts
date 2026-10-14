// @p1 @settings — organization profile write path (e2e plan P1-2).
//
// As dev-admin in tenant "E2E Org" (slug e2e-org): rename the org display
// name through Settings → Profile, save, reload, assert the new name
// persisted, then poll the inari-server audit API (helpers/api.ts — never UI
// scraping) until a matching audit event shows up. The org is SHARED, so the
// spec restores the original display name through the same UI path before it
// exits (spec independence — see README rule 4).
//
// Timeout budget: single test, dominated by the audit poll (60s / 5s — audit
// emission is asynchronous). The 120s test timeout keeps the project default
// (60s) from killing it.

import { expect, test } from "../../fixtures/failure-dump";
import type { Page } from "@playwright/test";

import { uniqueName } from "../../fixtures/org";
import { ApiClient } from "../../helpers/api";
import { TENANT_SLUG } from "../../helpers/env";
import { poll } from "../../helpers/poll";
import { AppShellPage } from "../../pages/app-shell.page";
import { OrgProfileSettingsPage } from "../../pages/settings/org-profile.page";

const NEW_NAME = uniqueName("org");

interface AuditEvent {
  action: string;
  objectType: string;
  objectName: string;
  detail: string;
  at: string;
}

/** Boot the console as dev-admin and land on Settings → Profile. */
async function gotoOrgProfile(page: Page): Promise<OrgProfileSettingsPage> {
  const shell = new AppShellPage(page);
  await page.goto(`/${TENANT_SLUG}/settings/org`, { waitUntil: "domcontentloaded" });
  await shell.waitForBoot();
  await shell.waitPastLoading();
  return new OrgProfileSettingsPage(page);
}

test("org display name write path persists and is audited @p1 @settings", async ({
  page,
}) => {
  test.setTimeout(120_000);

  // Resolve the CURRENT display name from the API (robust to TENANT_NAME
  // drift) so the restore step at the end is exact.
  const api = await ApiClient.forPersona();
  const tenants = await api.get<{ tenants?: { slug: string; displayName: string }[] }>(
    "/api/v1/tenants",
  );
  const original = (tenants.tenants ?? []).find((t) => t.slug === TENANT_SLUG)?.displayName;
  expect(original, `tenant ${TENANT_SLUG} resolvable via API`).toBeTruthy();

  // --- mutate through the UI ---
  const profile = await gotoOrgProfile(page);
  await poll(async () => expect(profile.displayNameInput()).toHaveValue(original!), {
    timeout: 20_000,
    interval: 1_000,
    message: "profile form loaded with current display name",
  });
  await profile.setDisplayName(NEW_NAME);
  await profile.save();
  await poll(async () => expect(profile.savedMessage()).toBeVisible(), {
    timeout: 20_000,
    interval: 1_000,
    message: "profile save confirmation",
  });

  // --- persisted across reload ---
  await page.reload({ waitUntil: "domcontentloaded" });
  await new AppShellPage(page).waitForBoot();
  await poll(async () => expect(profile.displayNameInput()).toHaveValue(NEW_NAME), {
    timeout: 30_000,
    interval: 1_000,
    message: "display name after reload",
  });

  // --- audit log entry via the REST API (propagates asynchronously) ---
  await poll(
    async () => {
      const body = await api.get<{ events?: AuditEvent[] | null }>(
        `/api/v1/tenants/${TENANT_SLUG}/audit`,
      );
      const hit = (body.events ?? []).find((e) =>
        [e.action, e.objectType, e.objectName, e.detail].join(" ").includes(NEW_NAME),
      );
      expect(hit, `audit event mentioning ${NEW_NAME}`).toBeTruthy();
    },
    { timeout: 60_000, interval: 5_000, message: "audit event for org rename" },
  );

  // --- restore the original name through the same UI path (shared org) ---
  await profile.setDisplayName(original!);
  await profile.save();
  // The second save hides and re-shows the saved card (component resets
  // `saved` on submit) and disables the button while the PATCH is in flight,
  // so require the card visible AND the button re-enabled.
  await poll(
    async () => {
      await expect(profile.savedMessage()).toBeVisible();
      await expect(profile.saveButton()).toBeEnabled();
    },
    { timeout: 20_000, interval: 1_000, message: "profile restore confirmation" },
  );
  await page.reload({ waitUntil: "domcontentloaded" });
  await new AppShellPage(page).waitForBoot();
  await poll(async () => expect(profile.displayNameInput()).toHaveValue(original!), {
    timeout: 30_000,
    interval: 1_000,
    message: "display name restored after reload",
  });
});
