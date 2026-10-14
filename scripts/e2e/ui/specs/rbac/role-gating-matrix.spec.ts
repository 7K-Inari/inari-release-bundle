// @p1 @rbac — per-persona RBAC role gating matrix (P1-1).
//
// For each of the four role personas (seed/seed-personas.mjs, one per
// ADR-0013 built-in role) the spec drives the console with that persona's
// storageState and asserts the visible/enabled control set matches its
// exact permission bundle from inari-server migration 0029_roles.sql:
//
//   admin    (dev-admin)    full control set — positive baseline
//   operator (e2e-operator) members management; NO teams/roles write, no
//                           tenant settings/identity/notifications writes
//   editor   (e2e-editor)   catalog/deploys/approvals surface; NO members
//                           write
//   viewer   (e2e-viewer)   everything read-only — write controls inert
//
// The bundle expectations are data tables below, so the spec doubles as a
// regression guard on the bundle definitions themselves (verified via the
// roles API as dev-admin). Negative navigation: direct-URL to write pages
// (Roles tab, Settings → OIDC clients / Notifications) as
// operator/editor/viewer must render read-only surfaces — never a 500 or a
// blank page.
//
// Read-only spec: it mutates nothing and never touches the personas' role
// assignments (README rule 4), so polling is unnecessary beyond the
// console's own loading gates. Capability gating in the console comes from
// inari-ui capability-gate.tsx (manageMembers/manageTeams/manageRbac) and
// settings-nav.ts (admin-only settings entries).

import { expect, test } from "../../fixtures/failure-dump";
import type { Page } from "@playwright/test";

import { PERSONAS, authFile, type Persona } from "../../fixtures/auth";
import { ApiClient } from "../../helpers/api";
import { TENANT_NAME, TENANT_SLUG } from "../../helpers/env";
import { AccessPage } from "../../pages/access/access.page";
import { IdentityTabPage } from "../../pages/access/identity.page";
import { MembersTabPage } from "../../pages/access/members.page";
import { RolesTabPage } from "../../pages/access/roles.page";
import { TeamsRolesTabPage } from "../../pages/access/teams-roles.page";
import { AppShellPage } from "../../pages/app-shell.page";
import { CatalogPage } from "../../pages/catalog.page";
import { DeploysPage } from "../../pages/deploys.page";
import { SettingsPage } from "../../pages/settings/settings.page";

// --- Permission bundles (regression guard) ---------------------------------
//
// Exact built-in bundles: migration 0029 seeded the originals; migration
// 0031 (kubectl gateway) appended clusters.kubectl to admin + operator.
// admin = the full static permission catalog; operator = admin minus the
// tenant-administration domain; editor/viewer as seeded. Order-insensitive —
// compared as sets.

const OPERATOR_PERMISSIONS = [
  "tenant.read",
  "tenant.members.manage",
  "clusters.register",
  "clusters.kubectl",
  "cloudaccounts.manage",
  "zones.manage",
  "fleet.manage",
  "policies.manage",
  "secretstores.manage",
  "extensions.manage",
  "extensions.invoke",
  "catalog.manage",
  "deployments.create",
  "approvals.manage",
];

const BUNDLES: Record<string, string[]> = {
  admin: [
    ...OPERATOR_PERMISSIONS.slice(0, 2),
    "tenant.settings.write",
    "tenant.teams.manage",
    "tenant.rbac.manage",
    "tenant.identity.manage",
    "tenant.notifications.manage",
    "tenant.admin",
    ...OPERATOR_PERMISSIONS.slice(2),
  ],
  operator: OPERATOR_PERMISSIONS,
  editor: [
    "tenant.read",
    "catalog.manage",
    "deployments.create",
    "extensions.invoke",
    "approvals.manage",
  ],
  viewer: ["tenant.read"],
};

// --- UI gating matrix -------------------------------------------------------
//
// Console capabilities derived from each bundle by
// capability-gate.tsx's /me/permissions projection. Note operator manages
// members but NOT teams (migration 0029 grants no tenant.teams.manage).

interface PersonaCase {
  key: keyof typeof PERSONAS;
  role: string;
  canManageMembers: boolean;
  canManageTeams: boolean;
  canManageRbac: boolean;
}

const MATRIX: PersonaCase[] = [
  { key: "devAdmin", role: "admin", canManageMembers: true, canManageTeams: true, canManageRbac: true },
  { key: "operator", role: "operator", canManageMembers: true, canManageTeams: false, canManageRbac: false },
  { key: "editor", role: "editor", canManageMembers: false, canManageTeams: false, canManageRbac: false },
  { key: "viewer", role: "viewer", canManageMembers: false, canManageTeams: false, canManageRbac: false },
];

// --- helpers ----------------------------------------------------------------

/** Boot the console straight into a tenant-scoped route (storageState skips
 *  the KC round-trip; the SPA still gates on "Loading…"). */
async function gotoTenantRoute(page: Page, route: string): Promise<AppShellPage> {
  const shell = new AppShellPage(page);
  await page.goto(`/${TENANT_SLUG}/${route}`, { waitUntil: "domcontentloaded" });
  await shell.waitForBoot();
  return shell;
}

/** Assert a settings write page renders its read-only surface (never a 500
 *  or a blank page) for a non-admin persona. */
async function expectSettingsReadOnly(
  page: Page,
  route: string,
  heading: string,
  writeButton: string,
): Promise<void> {
  const settings = new SettingsPage(page);
  await gotoTenantRoute(page, route);
  await expect(settings.heading(heading)).toBeVisible();
  await expect(settings.readOnlyNotice()).toBeVisible();
  await expect(page.getByRole("button", { name: writeButton, exact: true })).toHaveCount(0);
  // Not a crash/blank page: the SPA rendered real content, no error surface.
  await expect(page.locator("body")).not.toContainText(/internal server error|500/i);
}

// --- bundle regression guard (API, dev-admin) -------------------------------

test("seeded built-in role bundles match the migration-0029+0031 matrix @p1 @rbac", async () => {
  const api = await ApiClient.forPersona();
  const org = await api.resolveOrgSlug(TENANT_NAME);
  const listed = await api.get<{ roles?: { name: string; permissions?: string[] }[] | null }>(
    `/api/v1/tenants/${org}/roles`,
  );
  const byName = new Map((listed.roles ?? []).map((r) => [r.name, r.permissions ?? []]));

  for (const [role, expected] of Object.entries(BUNDLES)) {
    await test.step(`bundle "${role}"`, async () => {
      const actual = byName.get(role);
      expect(actual, `built-in role "${role}" must exist in E2E Org`).toBeTruthy();
      expect(
        [...(actual ?? [])].sort(),
        `bundle drift for built-in role "${role}" (migrations 0029+0031)`,
      ).toEqual([...expected].sort());
    });
  }
});

// --- per-persona UI matrix ----------------------------------------------------

for (const personaCase of MATRIX) {
  const persona: Persona = PERSONAS[personaCase.key];

  test.describe(`role gating: ${personaCase.role} (${persona.name}) @p1 @rbac`, () => {
    test.use({ storageState: authFile(persona) });

    test(`control set matches the ${personaCase.role} bundle @p1 @rbac`, async ({ page }) => {
      await gotoTenantRoute(page, "access/members");

      await test.step("Access → Members", async () => {
        const members = new MembersTabPage(page);
        await expect(members.marker()).toBeVisible();
        // "Read-only" badge renders iff the user cannot manage members.
        await expect(members.readOnlyBadge()).toHaveCount(personaCase.canManageMembers ? 0 : 1);
        await expect(members.anyMemberRoleSelect()).toBeEnabled({
          enabled: personaCase.canManageMembers,
        });
        await expect(members.inviteButton()).toBeEnabled({
          enabled: personaCase.canManageMembers,
        });
        // Team creation is gated on tenant.teams.manage — admin only.
        await expect(members.createTeamNameInput()).toBeEnabled({
          enabled: personaCase.canManageTeams,
        });
      });

      await test.step("Access → Teams & Roles", async () => {
        const access = new AccessPage(page);
        await access.selectTab(TeamsRolesTabPage.LABEL);
        const matrix = new TeamsRolesTabPage(page);
        await expect(matrix.marker()).toBeVisible();
        await expect(matrix.anyRoleSelect()).toBeEnabled({
          enabled: personaCase.canManageRbac,
        });
      });

      await test.step("Access → Roles", async () => {
        const access = new AccessPage(page);
        await access.selectTab(RolesTabPage.LABEL);
        const roles = new RolesTabPage(page);
        await expect(roles.marker()).toBeVisible();
        if (personaCase.canManageRbac) {
          await expect(roles.newRoleButton()).toBeEnabled();
        } else {
          // CapabilityGate disable-mode: inert, not hidden, not native-disabled.
          await expect(roles.inertControl("New role")).toBeVisible();
          await expect(roles.inertControl("New role")).toHaveAttribute(
            "title",
            "Requires role management permission",
          );
        }
      });

      await test.step("Access → Identity", async () => {
        const access = new AccessPage(page);
        await access.selectTab(IdentityTabPage.LABEL);
        await expect(new IdentityTabPage(page).marker()).toBeVisible();
      });

      await test.step("Settings surface", async () => {
        const settings = new SettingsPage(page);
        await settings.goto();
        await expect(page).toHaveURL(new RegExp(`/${TENANT_SLUG}/settings/`));
        if (personaCase.role === "admin") {
          // Admin-only settings entries are visible; write controls enabled.
          await expect(settings.navItem("OIDC Clients")).toBeVisible();
          await settings.navItem("OIDC Clients").click();
          await expect(settings.heading("OIDC clients")).toBeVisible();
          await expect(
            page.getByRole("button", { name: "New client", exact: true }),
          ).toBeEnabled();
        } else {
          // Admin-only entries are filtered out of the settings nav.
          await expect(settings.navItem("OIDC Clients")).toHaveCount(0);
          await expect(settings.navItem("IdP Brokering")).toHaveCount(0);
        }
      });

      await test.step("Catalog + Deploys read surfaces", async () => {
        const catalog = new CatalogPage(page);
        await catalog.goto();
        await expect(catalog.marker()).toBeVisible();
        const deploys = new DeploysPage(page);
        await deploys.goto();
        await expect(deploys.marker()).toBeVisible();
      });

      if (personaCase.role === "admin") {
        await test.step("positive baseline: write pages are fully usable", async () => {
          const settings = new SettingsPage(page);
          await gotoTenantRoute(page, "settings/notifications");
          await expect(settings.heading("Notifications")).toBeVisible();
          await expect(
            page.getByRole("button", { name: "New endpoint", exact: true }),
          ).toBeEnabled();
          await expect(settings.readOnlyNotice()).toHaveCount(0);
        });
      } else {
        await test.step("negative navigation: direct URL to write pages is read-only", async () => {
          await expectSettingsReadOnly(page, "settings/identity/clients", "OIDC clients", "New client");
          await expectSettingsReadOnly(page, "settings/notifications", "Notifications", "New endpoint");

          // Direct URL to the Roles tab: table renders, write controls inert.
          const roles = new RolesTabPage(page);
          await gotoTenantRoute(page, "access/roles");
          await expect(roles.marker()).toBeVisible();
          await expect(roles.inertControl("New role")).toBeVisible();
          await expect(page.locator("body")).not.toContainText(/internal server error|500/i);
        });
      }
    });
  });
}
