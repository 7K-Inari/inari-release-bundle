// @p1 @tenants — tenant switching via the app-shell switcher (e2e plan P1-5).
//
// As dev-admin: create a second org via the API in beforeAll (slug
// e2e-<runId>-<seq>-sw — self-contained, does NOT depend on P1-4's leftover
// org), then switch the tenant context e2e-org ↔ new org through the header
// tenant switcher and assert that data scopes correctly in each context:
//
//   - Access → Members lists e2e-member@inari.local in e2e-org but NOT in
//     the fresh org (only its creator dev-admin belongs to it), while the
//     tab's data marker renders in both;
//   - Settings → Profile (#org-slug, inari-ui src/pages/settings/org/
//     org-profile.tsx) shows the active tenant's slug;
//   - the switcher trigger label (current-tenant indicator) shows the active
//     org's display name after each switch — including after a full page
//     reload (tenant context is URL-carried; recents persist in
//     localStorage).
//
// LEFTOVER ORG — INTENTIONAL: DELETE /api/v1/tenants/{org} is approval-gated
// and asynchronous (decommission flow), so there is no clean synchronous
// delete to run in afterAll. The unique name is harmless on fresh CI
// clusters, same rationale as the onboarding spec.
//
// Switching is async end to end: selecting an org calls
// switchOrganization() (inari-ui src/auth/keycloak.ts), which re-logins via
// Keycloak (organization scope, prompt:none) — a FULL page redirect — and
// the switcher menu only lists orgs present in the KC token claim, which
// lags API creation. EVERY step below polls (helpers/poll.ts) and each poll
// retry is a fresh page load, which re-runs keycloak-js init and silently
// re-issues a token with current claims (same mechanism as the onboarding
// spec's landInTenant).
//
// Timeout budget: expected runtime ~120s, dominated by the KC-claim
// propagation polls around the two switches. The describe-level 300s test
// timeout keeps the project default (60s) from killing them.

import { expect, test } from "../../fixtures/failure-dump";
import type { Page } from "@playwright/test";

import { uniqueName } from "../../fixtures/org";
import { ApiClient } from "../../helpers/api";
import { TENANT_NAME, TENANT_SLUG } from "../../helpers/env";
import { poll } from "../../helpers/poll";
import { AccessPage } from "../../pages/access/access.page";
import { MembersTabPage } from "../../pages/access/members.page";
import { AppShellPage } from "../../pages/app-shell.page";

const SLUG = uniqueName("sw");
const DISPLAY_NAME = `E2E ${SLUG}`;

/** Seeded role-less persona (seed/seed-personas.mjs) — member of e2e-org only. */
const MEMBER_EMAIL = "e2e-member@inari.local";

interface ListedTenants {
  tenants?:
    | {
        slug: string;
        displayName: string;
        status: string;
        keycloakOrgId: string;
        createdAt: string;
      }[]
    | null;
}

/**
 * Land in a tenant context with fresh claims. Mirrors landInTenant() in
 * onboarding.spec.ts: each retry is a full page load, re-issuing a token
 * whose organization claim reflects current KC membership.
 */
async function landInTenant(page: Page, slug: string): Promise<AppShellPage> {
  const shell = new AppShellPage(page);
  await poll(
    async () => {
      if (!page.url().includes(`/${slug}/`)) {
        await page.goto(`/${slug}/overview`, { waitUntil: "domcontentloaded" });
        await shell.waitForBoot();
      }
      await expect(page).toHaveURL(new RegExp(`/${slug}/overview`), { timeout: 5_000 });
      return true;
    },
    { timeout: 90_000, interval: 5_000, message: `landing in /${slug}/overview` },
  );
  return shell;
}

/**
 * Switch tenant context through the header switcher and wait for the new
 * context to be active (URL + switcher indicator). The switch is a full KC
 * redirect; if the org is not yet in the token claim the menu item is
 * missing and the retry reloads the page to pick up a fresh token.
 */
async function switchToTenant(page: Page, slug: string, displayName: string): Promise<AppShellPage> {
  const shell = new AppShellPage(page);
  await poll(
    async () => {
      if (!page.url().includes(`/${slug}/`)) {
        try {
          await shell.switchTenant(displayName);
        } catch {
          // Org not listed yet (claim lag) or menu closed mid-redirect:
          // reload for a fresh token and let the next iteration retry.
          await page.reload({ waitUntil: "domcontentloaded" });
          await shell.waitForBoot();
        }
      }
      await expect(page).toHaveURL(new RegExp(`/${slug}/overview`), { timeout: 10_000 });
      // Current-tenant indicator reflects the active context.
      await expect(shell.switcherButton()).toContainText(displayName, { timeout: 5_000 });
      return true;
    },
    { timeout: 90_000, interval: 5_000, message: `switching to tenant ${slug}` },
  );
  return shell;
}

/** Assert the Members tab's data-loaded marker; optionally that the seeded
 * member is present (e2e-org) or scoped out (fresh org). */
async function assertMembersScope(page: Page, expectMember: boolean): Promise<void> {
  const access = new AccessPage(page);
  const members = new MembersTabPage(page);
  await access.goto();
  // Poll: the tab's data only renders once /me/permissions and the members
  // API agree on the new context after a switch.
  await poll(
    async () => {
      await access.selectTab(MembersTabPage.LABEL);
      await expect(members.marker()).toBeVisible({ timeout: 5_000 });
      if (expectMember) {
        await expect(page.getByText(MEMBER_EMAIL)).toBeVisible({ timeout: 5_000 });
      }
      return true;
    },
    {
      timeout: 60_000,
      interval: 5_000,
      message: `Members tab scope (member ${expectMember ? "present" : "absent"})`,
    },
  );
  if (!expectMember) {
    // Marker visible ⇒ the members fetch completed; the seeded e2e-org
    // member must not leak into the fresh org's list.
    await expect(page.getByText(MEMBER_EMAIL)).toHaveCount(0);
  }
}

/** Assert Settings → Profile shows the active tenant's slug. */
async function assertSettingsSlug(page: Page, slug: string): Promise<void> {
  const shell = new AppShellPage(page);
  await poll(
    async () => {
      await page.goto(`/${slug}/settings/org`, { waitUntil: "domcontentloaded" });
      await shell.waitPastLoading();
      await expect(page.locator("#org-slug")).toHaveValue(slug, { timeout: 5_000 });
      return true;
    },
    { timeout: 60_000, interval: 5_000, message: `Settings profile slug ${slug}` },
  );
}

test.describe.serial("tenant switching via the console switcher @p1 @tenants", () => {
  test.setTimeout(300_000);

  test.beforeAll(async ({}, testInfo) => {
    // Hook timeout is independent of the describe test timeout.
    testInfo.setTimeout(120_000);
    const api = await ApiClient.forPersona();
    // Spec-owned setup org via the API (same endpoint the console's
    // create-organization page uses) — never depends on P1-4 leftovers.
    await api.post<unknown>("/api/v1/tenants", { slug: SLUG, displayName: DISPLAY_NAME });
    // Org creation is async: keycloakOrgId is only set once KC organization
    // provisioning has run (same gate as onboarding.spec.ts). The browser's
    // switcher additionally needs the org in the token claim; the per-step
    // polls below cover that propagation.
    await poll(
      async () => {
        const body = await api.get<ListedTenants>("/api/v1/tenants");
        const match = (body.tenants ?? []).find((t) => t.slug === SLUG);
        expect(match?.keycloakOrgId, `org ${SLUG} with keycloakOrgId`).toBeTruthy();
        return true;
      },
      { timeout: 90_000, interval: 5_000, message: `org ${SLUG} KC provisioning` },
    );
  });

  test("baseline: e2e-org context scopes its data", async ({ page }) => {
    const shell = await landInTenant(page, TENANT_SLUG);
    await expect(shell.switcherButton()).toContainText(TENANT_NAME);
    await expect(page.getByText(new RegExp(`Tenant\\s+${TENANT_SLUG}`))).toBeVisible();
    await assertMembersScope(page, true);
    await assertSettingsSlug(page, TENANT_SLUG);
  });

  test("switching to the new org re-scopes members and settings", async ({ page }) => {
    await landInTenant(page, TENANT_SLUG);
    const shell = await switchToTenant(page, SLUG, DISPLAY_NAME);

    await expect(page.getByText(new RegExp(`Tenant\\s+${SLUG}`))).toBeVisible();
    // The fresh org has only its creator; the seeded e2e-org member is
    // scoped out, while the tab itself still renders its data marker.
    await assertMembersScope(page, false);
    await assertSettingsSlug(page, SLUG);
    // Current-tenant indicator still matches after the scoping checks.
    await expect(shell.switcherButton()).toContainText(DISPLAY_NAME);
  });

  test("tenant selection persists across a full page reload", async ({ page }) => {
    await switchToTenant(page, SLUG, DISPLAY_NAME);

    await page.reload({ waitUntil: "domcontentloaded" });
    const shell = new AppShellPage(page);
    await shell.waitForBoot();

    // Context is URL-carried, so a reload lands back in the same tenant and
    // the indicator still shows it. Poll: /me/permissions and cached queries
    // re-resolve after the reload.
    await poll(
      async () => {
        await expect(page).toHaveURL(new RegExp(`/${SLUG}/overview`), { timeout: 5_000 });
        await expect(shell.switcherButton()).toContainText(DISPLAY_NAME, { timeout: 5_000 });
        return true;
      },
      { timeout: 60_000, interval: 5_000, message: `context ${SLUG} after reload` },
    );
    await assertSettingsSlug(page, SLUG);
    await assertMembersScope(page, false);
  });

  test("switching back restores the e2e-org context and data", async ({ page }) => {
    await switchToTenant(page, SLUG, DISPLAY_NAME);
    const shell = await switchToTenant(page, TENANT_SLUG, TENANT_NAME);

    await expect(page.getByText(new RegExp(`Tenant\\s+${TENANT_SLUG}`))).toBeVisible();
    await assertMembersScope(page, true);
    await assertSettingsSlug(page, TENANT_SLUG);
    await expect(shell.switcherButton()).toContainText(TENANT_NAME);
  });
});
