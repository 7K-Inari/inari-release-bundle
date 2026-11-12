// Shared live-page registry for the @visual and @a11y layers. Navigation and
// locators only — specs own every assertion (README rule 2). No API stubbing:
// these suites deliberately exercise the real seeded environment.

import type { Locator, Page } from "@playwright/test";

import { TENANT_NAME, TENANT_SLUG } from "../helpers/env";
import { IdentityTabPage } from "../pages/access/identity.page";
import { MembersTabPage } from "../pages/access/members.page";
import { RolesTabPage } from "../pages/access/roles.page";
import { TeamsRolesTabPage } from "../pages/access/teams-roles.page";
import { AppShellPage } from "../pages/app-shell.page";
import { OrgProfileSettingsPage } from "../pages/settings/org-profile.page";

export interface KeyPage {
  /** Stable test/screenshot name (kebab-case; never a display label). */
  name: string;
  path: string;
  /** Element whose visibility means the route's real content has rendered. */
  marker(page: Page): Locator;
  /** Live data hidden from screenshots; never used to silence axe rules. */
  masks?(page: Page): Locator[];
  /** Optional stable capture region (avoids variable-height masked content). */
  clip?(page: Page): { x: number; y: number; width: number; height: number };
  /** Enter through the tenant card instead of deep-linking (dashboard only). */
  viaTenantCard?: boolean;
}

function shellMasks(page: Page): Locator[] {
  return [
    // Approval/cluster counts and the token-derived avatar are user/session
    // data, not layout. Mask them in every visual baseline.
    page.locator(".sidebar-item-badge"),
    page.locator(".avatar-trigger"),
  ];
}

export const KEY_PAGES: readonly KeyPage[] = [
  {
    name: "app-shell-home",
    path: "/",
    marker: (page) => page.getByRole("heading", { name: "All tenants", exact: true }),
    masks: shellMasks,
    // Org leftovers change the tenant strip/aggregate heights. Capture the
    // stable shell chrome above those regions; full-shell navigation remains
    // covered by the Access baseline below.
    clip: (page) => ({
      x: 0,
      y: 0,
      width: page.viewportSize()?.width ?? 1280,
      height: 150,
    }),
  },
  {
    name: "tenant-dashboard",
    path: `/${TENANT_SLUG}/overview`,
    viaTenantCard: true,
    marker: (page) => new AppShellPage(page).overviewTitle(),
    masks: (page) => [
      ...shellMasks(page),
      page.getByTestId("overview-stat-row"),
      page.locator(".overview-grid .card-body"),
    ],
  },
  {
    name: "access-members",
    path: `/${TENANT_SLUG}/access/members`,
    marker: (page) => new MembersTabPage(page).marker(),
  },
  {
    name: "access-teams-roles",
    path: `/${TENANT_SLUG}/access/teams`,
    marker: (page) => new TeamsRolesTabPage(page).marker(),
  },
  {
    name: "access-roles",
    path: `/${TENANT_SLUG}/access/roles`,
    marker: (page) => new RolesTabPage(page).marker(),
  },
  {
    name: "access-identity",
    path: `/${TENANT_SLUG}/access/identity`,
    marker: (page) => new IdentityTabPage(page).marker(),
    // Identity is the visual Access baseline: its static cards preserve the
    // complete four-tab shell without hiding live members/teams/roles data.
    masks: shellMasks,
  },
  {
    name: "settings-org-profile",
    path: `/${TENANT_SLUG}/settings/org`,
    // Save is admin-only and rendered with the loaded form. Waiting for it
    // avoids photographing the transient pre-/me/permissions read-only state.
    marker: (page) => new OrgProfileSettingsPage(page).saveButton(),
    masks: (page) => [
      ...shellMasks(page),
      // Another nightly shard may be running the org-profile write spec.
      new OrgProfileSettingsPage(page).displayNameInput(),
    ],
  },
] as const;

export const VISUAL_PAGES = KEY_PAGES.filter((keyPage) =>
  ["app-shell-home", "tenant-dashboard", "access-identity", "settings-org-profile"].includes(
    keyPage.name,
  ),
);

/** Open a live page and wait for content, fonts, and asynchronous skeletons. */
export async function openKeyPage(page: Page, keyPage: KeyPage): Promise<void> {
  await page.emulateMedia({ reducedMotion: "reduce" });
  const shell = new AppShellPage(page);

  if (keyPage.viaTenantCard) {
    await page.goto("/", { waitUntil: "domcontentloaded" });
    await shell.waitForBoot();
    await shell.openTenant(TENANT_NAME);
  } else {
    await page.goto(keyPage.path, { waitUntil: "domcontentloaded" });
    await shell.waitForBoot();
  }

  await keyPage.marker(page).waitFor({ state: "visible", timeout: 45_000 });
  await shell.waitPastLoading();
  await page.waitForFunction(
    () => document.querySelectorAll("[aria-busy='true']").length === 0,
    undefined,
    { timeout: 20_000 },
  );
  await page.evaluate(async () => {
    await document.fonts.ready;
    await new Promise<void>((resolve) => {
      requestAnimationFrame(() => requestAnimationFrame(() => resolve()));
    });
  });
}
