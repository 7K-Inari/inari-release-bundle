// App-shell page object: console boot gate, tenant switcher/cards, sidebar
// nav, and the "Loading…" waits. No assertions — specs call expect().

import type { Locator, Page } from "@playwright/test";

export class AppShellPage {
  constructor(readonly page: Page) {}

  /** Wait until the SPA has booted past its initial "Loading…" gate. */
  async waitForBoot(): Promise<void> {
    // Require non-empty text: on first paint the body is still empty (""),
    // which would trivially pass a plain !== "Loading…" check before the
    // SPA has rendered anything.
    await this.page.waitForFunction(
      () => document.body.innerText.trim().length > 0 && !document.body.innerText.includes("Loading…"),
      { timeout: 45_000 },
    );
  }

  /** Wait until no "Loading…" placeholder remains (post-navigation). */
  async waitPastLoading(): Promise<void> {
    await this.page.waitForFunction(
      () => !document.body.innerText.includes("Loading…"),
      { timeout: 15_000 },
    );
  }

  /** Click a tenant card on the switcher home by its display name. */
  async openTenant(name: string): Promise<void> {
    // Verified selection: on slow stacks the card click can land mid-render
    // and get dropped, leaving the app on the /all view ("Select a specific
    // tenant to load this view") — every downstream assertion then fails
    // against the wrong tenant (kind CI, traced via DOM dumps). Re-navigate
    // home and re-click until the switcher shows the tenant, bounded.
    const deadline = Date.now() + 60_000;
    for (;;) {
      const link = this.page.getByRole("link", { name: new RegExp(name) }).first();
      if ((await link.count()) === 0) {
        // Not on the switcher home (e.g. stuck on /all) — go home first.
        await this.page.goto("/", { waitUntil: "domcontentloaded" });
        await this.waitForBoot();
      }
      await this.page
        .getByRole("link", { name: new RegExp(name) })
        .first()
        .click();
      await this.waitPastLoading();
      const norm = (s: string) => s.toLowerCase().replace(/[^a-z0-9]+/g, "-");
      const active = await this.switcherButton()
        .textContent()
        .then((t) => norm(t ?? "").includes(norm(name)))
        .catch(() => false);
      if (active) return;
      if (Date.now() > deadline) {
        throw new Error(`openTenant: tenant "${name}" never became active`);
      }
      await this.page.waitForTimeout(1_000);
    }
  }

  /** Navigate via a sidebar link (exact name match, e.g. "Access"). */
  async nav(name: string): Promise<void> {
    await this.page.getByRole("link", { name, exact: true }).first().click();
  }

  /**
   * Open the "Create organization" page via the header tenant switcher
   * (inari-ui src/layout/tenant-switcher.tsx — the menu item renders only
   * for callers with canCreateOrganizations).
   */
  async openCreateOrganization(): Promise<void> {
    await this.switcherButton().click();
    await this.page.getByRole("menuitem", { name: "Create organization" }).click();
  }

  /**
   * The header tenant-switcher trigger. Its text is the current-context
   * indicator ("All tenants" or the active org display name, optionally
   * with a team suffix) — specs assert it after every switch.
   */
  switcherButton(): Locator {
    return this.page.getByRole("button", { name: "Tenant context switcher" });
  }

  /**
   * Switch tenant context by org display name via the header switcher
   * (inari-ui src/layout/tenant-switcher.tsx). Selecting an org triggers a
   * FULL Keycloak re-login (organization scope, prompt:none) that navigates
   * away and lands on /<slug>/overview, so callers MUST poll
   * (helpers/poll.ts) for the new context — never assert immediately. The
   * same org can render twice in the menu (Recent + Organizations
   * sections), hence .first().
   */
  async switchTenant(name: string): Promise<void> {
    await this.switcherButton().click();
    await this.page.getByRole("menuitem", { name, exact: true }).first().click();
  }
}
