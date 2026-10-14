// Access → Roles tab page object. Marker text ("Built-in") and the editor /
// table markup come from inari-ui src/pages/access/roles-tab.tsx. No
// assertions.

import { expect, type Locator, type Page } from "@playwright/test";

export class RolesTabPage {
  static readonly LABEL = "Roles";
  static readonly MARKER = "Built-in";

  constructor(readonly page: Page) {}

  /** Text that renders once the tab's data has loaded. */
  marker(): Locator {
    return this.page.getByText(RolesTabPage.MARKER).first();
  }

  /** Table row for a role (matches display name and/or slug). */
  roleRow(name: string): Locator {
    return this.page.getByRole("row", { name: new RegExp(name) });
  }

  /** "Built-in" badge inside a role row. */
  builtinBadge(row: Locator): Locator {
    return row.getByText("Built-in", { exact: true });
  }

  /** Edit button inside a role row. */
  editButton(row: Locator): Locator {
    return row.getByRole("button", { name: "Edit", exact: true });
  }

  /**
   * Delete button inside a role row. Only rendered for custom roles
   * (roles-tab.tsx gates it on `!role.builtin`); specs assert absence.
   */
  deleteButton(row: Locator): Locator {
    return row.getByRole("button", { name: "Delete", exact: true });
  }

  /** Destructive error surface (save/delete API errors, validation). */
  errorMessage(): Locator {
    return this.page.locator(".text-destructive").first();
  }

  /** "New role" button — only rendered while no editor is open, so its
   *  visibility doubles as the "editor closed (save settled)" signal. */
  newRoleButton(): Locator {
    return this.page.getByRole("button", { name: "New role" });
  }

  /**
   * CapabilityGate disable-mode wrapper (span[aria-disabled="true"])
   * around a control. Users without manage-rbac see write controls inert
   * (pointer-events-none), not hidden and not natively disabled.
   */
  inertControl(name: string | RegExp): Locator {
    return this.page.locator('span[aria-disabled="true"]', {
      has: this.page.getByRole("button", { name }),
    });
  }

  /** Open the create-role editor. */
  async newRole(): Promise<void> {
    await expect(this.newRoleButton()).toBeEnabled({ timeout: 60_000 });
    await this.newRoleButton().click();
  }

  /** Open the editor for an existing role. */
  async openEditor(roleName: string): Promise<void> {
    const btn = this.roleRow(roleName).first().getByRole("button", { name: "Edit" });
    await expect(btn).toBeEnabled({ timeout: 60_000 });
    await btn.click();
  }

  /** "Name" input inside the role editor card. */
  editorNameInput(): Locator {
    return this.page.locator("#role-name");
  }

  /** Cancel button inside the role editor card. */
  editorCancelButton(): Locator {
    return this.page.getByRole("button", { name: "Cancel", exact: true });
  }

  /** Checkbox for one permission slug in the open editor. */
  permissionCheckbox(slug: string): Locator {
    return this.page
      .locator("label", { has: this.page.getByText(slug, { exact: true }) })
      .getByRole("checkbox");
  }

  async togglePermission(slug: string): Promise<void> {
    await this.permissionCheckbox(slug).click();
  }

  /** Fill the editor's scalar fields (all optional; empty string clears). */
  async fillEditor(fields: {
    name?: string;
    displayName?: string;
    description?: string;
  }): Promise<void> {
    if (fields.name !== undefined) {
      await this.page.locator("#role-name").fill(fields.name);
    }
    if (fields.displayName !== undefined) {
      await this.page.locator("#role-display-name").fill(fields.displayName);
    }
    if (fields.description !== undefined) {
      await this.page.locator("#role-description").fill(fields.description);
    }
  }

  /** Submit the open editor ("Create" for new roles, "Save" for edits). */
  async submitEditor(): Promise<void> {
    await this.page.getByRole("button", { name: /^(Create|Save)$/ }).click();
  }

  /**
   * Click Delete on a role's row. The UI guards with window.confirm — the
   * caller must register `page.once("dialog", ...)` before invoking this.
   */
  async deleteRole(roleName: string): Promise<void> {
    // Capability-gated: renders disabled until /me/permissions resolves —
    // wait for enabled or a slow permissions fetch fails as an opaque
    // click timeout (the 2026-10-14 kind stall class).
    const btn = this.roleRow(roleName).first().getByRole("button", { name: "Delete" });
    await expect(btn).toBeEnabled({ timeout: 60_000 });
    await btn.click();
  }
}
