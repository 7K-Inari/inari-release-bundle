// Access → Teams & Roles tab page object. Marker text ("Assign each team a
// role") and the matrix markup come from inari-ui
// src/pages/access/role-matrix.tsx. Enabled-waits only (capability-gate settle); no content assertions.

import { expect, type Locator, type Page } from "@playwright/test";

export class TeamsRolesTabPage {
  static readonly LABEL = "Teams & Roles";
  static readonly MARKER = "Assign each team a role";

  constructor(readonly page: Page) {}

  /** Text that renders once the tab's data has loaded. */
  marker(): Locator {
    return this.page.getByText(TeamsRolesTabPage.MARKER).first();
  }

  /** First per-team role select in the matrix (any team). */
  anyRoleSelect(): Locator {
    return this.page.getByLabel(/^Role for /).first();
  }

  /** Per-team role select (aria-label "Role for <team display name>"). */
  roleSelect(teamDisplay: string): Locator {
    return this.page.getByLabel(`Role for ${teamDisplay}`);
  }

  /** Pick a role for a team by its option label (display name or slug). */
  async selectRoleForTeam(teamDisplay: string, roleLabel: string): Promise<void> {
    await expect(this.roleSelect(teamDisplay)).toBeEnabled({ timeout: 60_000 });
    await this.roleSelect(teamDisplay).selectOption({ label: roleLabel });
  }

  saveButton(): Locator {
    return this.page.getByRole("button", { name: "Save changes" });
  }

  /** Persist the drafted matrix (whole-set replace server-side). */
  async saveChanges(): Promise<void> {
    await expect(this.saveButton()).toBeEnabled({ timeout: 60_000 });
    await this.saveButton().click();
  }

  /** "Discard" button, visible only while a dirty draft exists. */
  discardButton(): Locator {
    return this.page.getByRole("button", { name: "Discard", exact: true });
  }

  /** Tab-level error card showing the server's rejection message. */
  errorCard(): Locator {
    return this.page.locator(".text-destructive").first();
  }
}
