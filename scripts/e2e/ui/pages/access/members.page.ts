// Access → Members tab page object. Marker text ("Org-wide membership") and
// the Teams section markup come from inari-ui
// src/pages/access/members-tab.tsx. The only assertion is the enabled-wait in showMembers (capability-gate settle, not a post-mutation check).

import { expect, type Locator, type Page } from "@playwright/test";

export class MembersTabPage {
  static readonly LABEL = "Members";
  static readonly MARKER = "Org-wide membership";

  constructor(readonly page: Page) {}

  /** Text that renders once the tab's data has loaded. */
  marker(): Locator {
    return this.page.getByText(MembersTabPage.MARKER).first();
  }

  /** Card for a team in the Teams section (anchored on the slug text). */
  teamCard(teamName: string): Locator {
    return this.page
      .getByText(teamName, { exact: true })
      .locator("xpath=ancestor::*[contains(@class,'border')][1]");
  }

  /** A member entry inside a team's expanded Members panel. */
  memberEntry(teamName: string, memberText: string): Locator {
    return this.teamCard(teamName).getByText(memberText).first();
  }

  /** "Read-only" badge shown next to the Members heading when the user
   *  lacks the manage-members capability (members-tab.tsx). */
  readOnlyBadge(): Locator {
    return this.page.getByText("Read-only", { exact: true }).first();
  }

  /** First member role select (aria-label "Role for <displayName>"). */
  anyMemberRoleSelect(): Locator {
    return this.page.getByLabel(/^Role for /).first();
  }

  /** Submit button of the invite form. Disabled for users without the
   *  manage-members capability. */
  inviteButton(): Locator {
    return this.page.getByRole("button", { name: "Invite", exact: true });
  }

  /** Name input of the "Create team" form. Disabled for users without the
   *  manage-teams capability. */
  createTeamNameInput(): Locator {
    return this.page.locator("#team-name");
  }

  /** Create a team via the "Create team" form (role select left at its
   *  default; the Teams & Roles matrix owns team→role mapping). */
  async createTeam(name: string): Promise<void> {
    await this.page.locator("#team-name").fill(name);
    await this.page.getByRole("button", { name: "Create", exact: true }).click();
  }

  /** Expand a team's Members panel. */
  async showMembers(teamName: string): Promise<void> {
    // The card's buttons render DISABLED until /me/permissions resolves
    // (CapabilityGate mode="disable") — a plain click hangs on the
    // enabled-actionability check forever when that fetch is slow. Wait
    // for enabled first so a permissions-fetch stall fails with a clear
    // message instead of an opaque click timeout.
    const btn = this.teamCard(teamName).getByRole("button", { name: "Members" });
    await expect(btn).toBeEnabled({ timeout: 60_000 });
    await btn.click();
  }

  /** Add an org member to a team via the debounced search picker. */
  async addMemberToTeam(teamName: string, query: string): Promise<void> {
    await this.page.locator(`#picker-${teamName}`).fill(query);
    // The picker debounces 250ms before the result list renders.
    await this.page
      .getByRole("button", { name: new RegExp(query) })
      .first()
      .click();
  }
}
