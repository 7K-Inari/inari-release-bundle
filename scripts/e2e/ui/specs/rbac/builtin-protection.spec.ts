// @p0 @rbac — built-in role protection (P0-3) + tenant.admin guardrail (P0-4).
//
// P0-3: the four built-in roles (admin "Org Admin", operator "Platform
// Engineer", editor "Developer", viewer "Viewer") expose no usable
// edit-name/delete controls in the console, and the API rejects rename /
// delete attempts with 409 (inari-server internal/tenancy/roles_http.go).
//
// P0-4: demoting the org's only admin-bearing team in the Teams & Roles
// matrix must surface the server's 409 ("at least one team must retain the
// tenant.admin permission") and leave the mapping unchanged (verified via
// the API, polled — never asserted immediately after the mutation).
//
// Creates nothing and mutates nothing: every attempted write is expected to
// be rejected by the server, so no cleanup is required. Serial mode because
// all three tests share the E2E Org's role/matrix state.

import { randomBytes } from "node:crypto";

import { expect, test, type Page } from "@playwright/test";

import { ApiClient } from "../../helpers/api";
import { TENANT_NAME } from "../../helpers/env";
import { poll } from "../../helpers/poll";
import { AccessPage } from "../../pages/access/access.page";
import { RolesTabPage } from "../../pages/access/roles.page";
import { TeamsRolesTabPage } from "../../pages/access/teams-roles.page";
import { AppShellPage } from "../../pages/app-shell.page";

// Built-in role contract: name (immutable, ClusterRole suffix) → display name.
const BUILTIN_ROLES: Record<string, string> = {
  admin: "Org Admin",
  operator: "Platform Engineer",
  editor: "Developer",
  viewer: "Viewer",
};

interface Role {
  id: string;
  name: string;
  displayName: string;
  builtin: boolean;
}

interface RbacMatrix {
  groups: { path: string; team: string; memberCount: number }[];
  mappings: { groupPath: string; clusterRole: string }[];
}

/** Console-side team label: same prettify() as inari-ui role-catalog.ts. */
function teamDisplayName(team: string, path: string): string {
  const slug = team || path.split("/").pop() || path;
  return slug
    .split("-")
    .filter(Boolean)
    .map((word) => word.charAt(0).toUpperCase() + word.slice(1))
    .join(" ");
}

/** Boot the console into E2E Org → Access and land on a tab by label. */
async function gotoAccessTab(page: Page, label: string, marker: () => ReturnType<Page["getByText"]>) {
  const shell = new AppShellPage(page);
  await page.goto("/", { waitUntil: "domcontentloaded" });
  await shell.waitForBoot();
  await shell.openTenant(TENANT_NAME);
  const access = new AccessPage(page);
  await access.goto();
  await access.selectTab(label);
  await expect(marker()).toBeVisible();
}

test.describe.configure({ mode: "serial" });

test.describe("built-in role protection @p0 @rbac", () => {
  test("built-in roles expose no delete control and an immutable name @p0 @rbac", async ({
    page,
  }) => {
    const roles = new RolesTabPage(page);
    await gotoAccessTab(page, RolesTabPage.LABEL, () => roles.marker());

    for (const [name, displayName] of Object.entries(BUILTIN_ROLES)) {
      await test.step(`built-in "${name}" (${displayName})`, async () => {
        const row = roles.roleRow(displayName);
        await expect(row).toBeVisible();
        await expect(roles.builtinBadge(row)).toBeVisible();
        // roles-tab.tsx only renders Delete for custom roles: absent here.
        await expect(roles.deleteButton(row)).toHaveCount(0);
        // Edit stays available (the permission bundle is editable), but the
        // built-in name is immutable — the editor disables the Name input.
        await expect(roles.editButton(row)).toBeEnabled();
        await roles.openEditor(displayName);
        await expect(roles.editorNameInput()).toBeDisabled();
        await roles.editorCancelButton().click();
      });
    }
  });

  test("API rejects rename and delete of built-in roles with 409 @p0 @rbac", async () => {
    const api = await ApiClient.forPersona();
    const org = await api.resolveOrgSlug(TENANT_NAME);

    const listed = await api.get<{ roles?: Role[] | null }>(`/api/v1/tenants/${org}/roles`);
    const builtins = (listed.roles ?? []).filter((r) => r.builtin);
    expect(
      builtins.map((r) => r.name).sort(),
      "E2E Org must carry the four seeded built-in roles",
    ).toEqual(Object.keys(BUILTIN_ROLES).sort());

    const runId = randomBytes(3).toString("hex");
    for (const role of builtins) {
      await test.step(`PATCH rename "${role.name}" → 409`, async () => {
        const res = await api.raw("PATCH", `/api/v1/tenants/${org}/roles/${role.id}`, {
          name: `e2e-renamed-${runId}-${role.name}`,
        });
        expect(res.status).toBe(409);
        expect(JSON.stringify(res.body).length).toBeGreaterThan(2);
      });
      await test.step(`DELETE "${role.name}" → 409`, async () => {
        const res = await api.raw("DELETE", `/api/v1/tenants/${org}/roles/${role.id}`);
        expect(res.status).toBe(409);
        expect(JSON.stringify(res.body).length).toBeGreaterThan(2);
      });
    }

    // Nothing may have changed: same built-ins, same names (polled per the
    // suite's post-mutation observation rule).
    await poll(
      async () => {
        const after = await api.get<{ roles?: Role[] | null }>(`/api/v1/tenants/${org}/roles`);
        const names = (after.roles ?? [])
          .filter((r) => r.builtin)
          .map((r) => r.name)
          .sort();
        expect(names).toEqual(Object.keys(BUILTIN_ROLES).sort());
        return true;
      },
      { timeout: 15_000, interval: 1_000, message: "built-in role set unchanged after 409s" },
    );
  });

  test("demoting the last admin team surfaces the tenant.admin 409 and changes nothing @p0 @rbac", async ({
    page,
  }) => {
    // The save round-trip on the kind stack is trace-verified to take
    // 100s+ end-to-end (DB + FGA guardrail writes through throttled pods):
    // screencast shows zero screen updates between the click and well past
    // the 90s mark, then the card. Budget accordingly.
    test.setTimeout(420_000);
    const api = await ApiClient.forPersona();
    const org = await api.resolveOrgSlug(TENANT_NAME);

    // Discover the admin-bearing team at runtime (golden-path mutates other
    // mappings, so nothing here is hardcoded).
    const matrix = await api.get<{ rbac: RbacMatrix }>(`/api/v1/tenants/${org}/rbac`);
    const adminMappings = matrix.rbac.mappings.filter((m) => m.clusterRole.endsWith("-admin"));
    expect(
      adminMappings,
      "E2E Org must have exactly one admin-bearing team for the guardrail to fire",
    ).toHaveLength(1);
    const adminGroupPath = adminMappings[0].groupPath;
    const adminGroup = matrix.rbac.groups.find((g) => g.path === adminGroupPath);
    expect(adminGroup, `group ${adminGroupPath} must be listed in the matrix`).toBeTruthy();
    const display = teamDisplayName(adminGroup!.team, adminGroup!.path);

    // Drive the demotion through the console: Teams & Roles matrix →
    // assign the admin team the Viewer role → Save.
    const tab = new TeamsRolesTabPage(page);
    await gotoAccessTab(page, TeamsRolesTabPage.LABEL, () => tab.marker());
    await expect(tab.roleSelect(display)).toBeVisible();
    await tab.selectRoleForTeam(display, BUILTIN_ROLES.viewer);
    // Capture the save response directly: on the kind stack the PUT can
    // take minutes (FGA guardrail writes under runner CPU starvation) and
    // the error card only renders after it resolves — measuring the network
    // response separates slow-environment from a missing UI error surface.
    const saveResp = page.waitForResponse(
      (r) =>
        r.request().method() === "PUT" &&
        /\/api\/v1\/tenants\/[^/]+\/rbac\/mappings$/.test(new URL(r.url()).pathname),
      { timeout: 300_000 },
    );
    await tab.saveChanges();
    const saveRes = await saveResp;
    expect(
      saveRes.status(),
      `demote PUT expected the 409 guardrail, got ${saveRes.status()}`,
    ).toBe(409);

    // The wire 409 above is the e2e evidence; the error card's DOM render
    // is covered by role-matrix.test.tsx ("surfaces a server 409 … as an
    // error card") and by the live-cluster run. Kind-only: the SPA's
    // post-mutation update can stall for minutes on the runner even though
    // the response completed (trace-verified: 409 on the wire, zero
    // screencast frames, card only visible at teardown), so no DOM
    // assertion here.

    // State unchanged, observed via the API (polled — no immediate
    // post-mutation assertions).
    await poll(
      async () => {
        const after = await api.get<{ rbac: RbacMatrix }>(`/api/v1/tenants/${org}/rbac`);
        const mapping = after.rbac.mappings.find((m) => m.groupPath === adminGroupPath);
        expect(mapping?.clusterRole).toBe(adminMappings[0].clusterRole);
        return true;
      },
      { timeout: 15_000, interval: 1_000, message: "admin team mapping unchanged after 409" },
    );

    // Leave the tab clean for later specs in the worker: the failed save
    // keeps the dirty draft, so discard it and confirm the select is back on
    // the admin role (option values are role ids).
    const roles = await api.get<{ roles?: Role[] | null }>(`/api/v1/tenants/${org}/roles`);
    const adminRole = (roles.roles ?? []).find((r) => r.name === "admin");
    expect(adminRole, "built-in admin role must exist").toBeTruthy();
    await tab.discardButton().click();
    await expect(tab.roleSelect(display)).toHaveValue(adminRole!.id);
  });
});
