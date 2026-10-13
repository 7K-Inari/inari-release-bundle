// @p0 @rbac — RBAC role lifecycle + permission propagation (e2e plan P0-2).
//
// As dev-admin in tenant "E2E Org" (slug e2e-org): create a custom role over
// a subset of the ADR-0013 19-slug permission catalog, edit it, verify the
// permission bundle persists across reload, create a team, map team → role,
// add the seeded role-less `e2e-member` to the team, then poll
// GET /me/permissions with e2e-member's token until the custom role shows up
// (OrgTeamSync is interval-based — NEVER assert immediately, see
// helpers/poll.ts). Finally verify the delete-in-use guardrail (409 per
// ADR-0013: "custom roles bound to teams cannot be deleted") surfaces in the
// UI, and clean up spec-created objects so e2e-member ends role-less again.
//
// Timeout budget: expected runtime ~90s, dominated by the propagation poll
// (120s timeout / 5s interval covers the OrgTeamSync interval plus the
// /me/permissions cache TTL). The describe-level 180s test timeout keeps the
// default 60s project timeout from killing the poll.

import { expect, test, type Page } from "@playwright/test";

import { PERSONAS } from "../../fixtures/auth";
import { uniqueName } from "../../fixtures/org";
import { ApiClient } from "../../helpers/api";
import { TENANT_NAME, TENANT_SLUG } from "../../helpers/env";
import { poll } from "../../helpers/poll";
import { AccessPage } from "../../pages/access/access.page";
import { MembersTabPage } from "../../pages/access/members.page";
import { RolesTabPage } from "../../pages/access/roles.page";
import { TeamsRolesTabPage } from "../../pages/access/teams-roles.page";
import { AppShellPage } from "../../pages/app-shell.page";

const ROLE_NAME = uniqueName("role");
const TEAM_NAME = uniqueName("team");
const MEMBER = PERSONAS.member;

/** Mirrors teamDisplayName() in inari-ui src/pages/access/role-catalog.ts. */
function teamDisplayName(slug: string): string {
  return slug
    .split("-")
    .filter(Boolean)
    .map((w) => w.charAt(0).toUpperCase() + w.slice(1))
    .join(" ");
}

/** Boot the console as dev-admin and land on an Access tab. */
async function gotoAccessTab(page: Page, tab: string): Promise<void> {
  const shell = new AppShellPage(page);
  await page.goto("/", { waitUntil: "domcontentloaded" });
  await shell.waitForBoot();
  await shell.openTenant(TENANT_NAME);
  const access = new AccessPage(page);
  await access.goto();
  await access.selectTab(tab);
}

interface PermissionCatalog {
  permissions?: { slug: string }[];
}

interface MyPermissions {
  roles?: Record<string, string[]>;
}

test.describe.serial("RBAC role lifecycle and propagation @p0 @rbac", () => {
  // 300s: the create test alone budgets 90s for the POST + 120s for the row
  // poll on a cold kind runner.
  test.setTimeout(300_000);

  let slugs: string[] = [];

  test.beforeAll(async () => {
    const api = await ApiClient.forPersona();
    const catalog = await api.get<PermissionCatalog>(
      `/tenants/${TENANT_SLUG}/permissions/catalog`,
    );
    slugs = (catalog.permissions ?? []).map((p) => p.slug);
  });

  test.afterAll(async () => {
    // Best-effort cleanup of spec-created objects (delete order matters:
    // the role is only deletable once no team is bound to it). Restores
    // e2e-member to role-less. Never mask the test result.
    try {
      const api = await ApiClient.forPersona();
      await api.delete(`/tenants/${TENANT_SLUG}/teams/${TEAM_NAME}`).catch(() => {});
      await api.delete(`/tenants/${TENANT_SLUG}/roles/${ROLE_NAME}`).catch(() => {});
    } catch {
      // ignore — cleanup is best-effort
    }
  });

  test("permission catalog exposes the ADR-0013 slugs", () => {
    // Membership, not an exact count: the catalog legitimately grows with
    // the platform (clusters.kubectl arrived with the kubectl gateway).
    // An exact-length assertion breaks on every additive permission.
    for (const required of [
      "tenant.read",
      "tenant.members.manage",
      "tenant.teams.manage",
      "tenant.rbac.manage",
      "tenant.admin",
      "clusters.register",
    ]) {
      expect(slugs).toContain(required);
    }
  });

  test("create a custom role over a subset of the catalog", async ({ page }) => {
    await gotoAccessTab(page, RolesTabPage.LABEL);
    const roles = new RolesTabPage(page);
    await expect(roles.marker()).toBeVisible();

    await roles.newRole();
    await roles.fillEditor({
      name: ROLE_NAME,
      displayName: `E2E ${ROLE_NAME}`,
      description: "e2e role-lifecycle spec (auto-cleanup)",
    });
    for (const slug of slugs.slice(0, 3)) {
      await roles.togglePermission(slug);
    }
    // Capture the create response explicitly: the POST can take tens of
    // seconds through the ui-proxy port-forwards on a cold runner (verified
    // in CI — a 60s poll expired while the row rendered just after), so the
    // poll below must start from a resolved create, not from the click.
    const created = page.waitForResponse(
      (r) =>
        r.request().method() === "POST" &&
        /\/api\/v1\/tenants\/[^/]+\/roles$/.test(new URL(r.url()).pathname),
      { timeout: 90_000 },
    );
    await roles.submitEditor();
    const createRes = await created;
    expect(
      createRes.ok(),
      `create role POST returned ${createRes.status()}: ${await createRes.text().catch(() => "")}`,
    ).toBeTruthy();

    await poll(
      async () => expect(roles.roleRow(ROLE_NAME).first()).toBeVisible(),
      // 120s: covers editor close + list refetch after a slow create.
      { timeout: 120_000, interval: 1_000, message: "role row after create" },
    );
  });

  test("edit the role's permissions; the bundle persists after reload", async ({
    page,
  }) => {
    await gotoAccessTab(page, RolesTabPage.LABEL);
    const roles = new RolesTabPage(page);

    await roles.openEditor(ROLE_NAME);
    await poll(
      async () => expect(roles.permissionCheckbox(slugs[0])).toBeChecked(),
      { timeout: 20_000, interval: 1_000, message: "editor pre-edit state" },
    );
    // Toggle one off, one on.
    await roles.togglePermission(slugs[0]);
    await roles.togglePermission(slugs[3]);
    await roles.submitEditor();
    // The editor closes (onDone → "New role" re-renders) only after the
    // PATCH resolves — wait for that before reloading, or the navigation
    // can abort the in-flight save. On failure the editor stays open and
    // this times out with the real error still on screen.
    await poll(
      async () => expect(roles.newRoleButton()).toBeVisible(),
      { timeout: 20_000, interval: 1_000, message: "editor closed after save" },
    );

    // Reload and verify the persisted bundle.
    await page.reload({ waitUntil: "domcontentloaded" });
    await new AppShellPage(page).waitForBoot();
    await expect(roles.marker()).toBeVisible();
    await roles.openEditor(ROLE_NAME);

    await poll(
      async () => {
        await expect(roles.permissionCheckbox(slugs[0])).not.toBeChecked();
        await expect(roles.permissionCheckbox(slugs[1])).toBeChecked();
        await expect(roles.permissionCheckbox(slugs[2])).toBeChecked();
        await expect(roles.permissionCheckbox(slugs[3])).toBeChecked();
      },
      { timeout: 20_000, interval: 1_000, message: "permission bundle after reload" },
    );
  });

  test("create a team", async ({ page }) => {
    await gotoAccessTab(page, MembersTabPage.LABEL);
    const members = new MembersTabPage(page);
    await expect(members.marker()).toBeVisible();

    await members.createTeam(TEAM_NAME);
    await poll(
      async () => expect(members.teamCard(TEAM_NAME)).toBeVisible(),
      { timeout: 20_000, interval: 1_000, message: "team card after create" },
    );
  });

  test("map team to the custom role and save the matrix", async ({ page }) => {
    await gotoAccessTab(page, TeamsRolesTabPage.LABEL);
    const matrix = new TeamsRolesTabPage(page);
    await expect(matrix.marker()).toBeVisible();

    const display = teamDisplayName(TEAM_NAME);
    await poll(
      async () => expect(matrix.roleSelect(display)).toBeVisible(),
      { timeout: 20_000, interval: 1_000, message: "team row in matrix" },
    );
    await matrix.selectRoleForTeam(display, `E2E ${ROLE_NAME}`);
    await matrix.saveChanges();

    // The select is bound to the client-side draft, which clears only after
    // the PUT resolves. Wait for the settled state — disabled AND back to
    // "Save changes" (while the PUT is in flight the button is disabled but
    // reads "Saving…") — otherwise the re-read below observes the unsaved
    // draft and a failed save would only surface 120s later in the
    // propagation poll.
    await poll(
      async () => {
        await expect(matrix.saveButton()).toBeDisabled();
        await expect(matrix.saveButton()).toHaveText("Save changes");
      },
      { timeout: 20_000, interval: 1_000, message: "matrix save settled (draft cleared)" },
    );

    // After the whole-set replace the matrix refetches; the row must re-read
    // with the custom role selected (not the "No role" sentinel).
    await poll(
      async () => {
        const selected = await matrix
          .roleSelect(display)
          .evaluate((el) => (el as HTMLSelectElement).selectedOptions[0]?.textContent ?? "");
        return selected.includes(ROLE_NAME);
      },
      { timeout: 20_000, interval: 1_000, message: "matrix re-read with custom role" },
    );
  });

  test("add e2e-member to the team", async ({ page }) => {
    await gotoAccessTab(page, MembersTabPage.LABEL);
    const members = new MembersTabPage(page);

    await members.showMembers(TEAM_NAME);
    await members.addMemberToTeam(TEAM_NAME, MEMBER.username);
    await poll(
      async () =>
        expect(members.memberEntry(TEAM_NAME, MEMBER.username)).toBeVisible(),
      { timeout: 20_000, interval: 1_000, message: "member listed in team" },
    );
  });

  test("custom role propagates to e2e-member's /me/permissions", async () => {
    // OrgTeamSync + the /me/permissions cache are interval-based: poll with
    // a 120s budget (see header). Uses e2e-member's direct-grant token.
    const memberApi = await ApiClient.forPersona(MEMBER.username, MEMBER.password);
    await poll(
      async () => {
        const me = await memberApi.get<MyPermissions>("/me/permissions");
        return me.roles?.[TENANT_SLUG]?.includes(ROLE_NAME) ?? false;
      },
      {
        timeout: 120_000,
        interval: 5_000,
        message: `custom role ${ROLE_NAME} in e2e-member /me/permissions`,
      },
    );
  });

  test("deleting the in-use role surfaces the 409 guardrail", async ({ page }) => {
    await gotoAccessTab(page, RolesTabPage.LABEL);
    const roles = new RolesTabPage(page);

    page.once("dialog", (dialog) => void dialog.accept());
    await roles.deleteRole(ROLE_NAME);

    await poll(
      async () => {
        await expect(roles.errorMessage()).toBeVisible();
        await expect(roles.errorMessage()).toContainText(/team|in use|bound/i);
      },
      { timeout: 20_000, interval: 1_000, message: "delete-in-use 409 surfaced" },
    );
    // The role must still exist — the deletion was rejected.
    await expect(roles.roleRow(ROLE_NAME).first()).toBeVisible();
  });
});
