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
    // against the wrong tenant (kind CI, traced via DOM dumps). Selecting
    // an org triggers a FULL Keycloak re-login (organization scope,
    // prompt:none) that navigates away and lands on /<slug>/overview, so
    // verify by URL once the redirect cycle settles — never by reading the
    // switcher mid-redirect. Re-navigate and re-click until landed,
    // bounded.
    const deadline = Date.now() + 90_000;
    for (;;) {
      const link = this.page.getByRole("link", { name: new RegExp(name) }).first();
      if ((await link.count()) === 0) {
        // Not on the switcher home (e.g. stuck on /all) — go home first.
        await this.page.goto("/", { waitUntil: "domcontentloaded" });
        await this.waitForBoot();
      }
      const href = await this.page
        .getByRole("link", { name: new RegExp(name) })
        .first()
        .getAttribute("href");
      const slug = href?.replace(/^\//, "").split("/")[0] || "";
      await this.page
        .getByRole("link", { name: new RegExp(name) })
        .first()
        .click();
      try {
        await this.page.waitForURL(
          (u) => new URL(u).pathname.startsWith(`/${slug}/`),
          { timeout: 30_000 },
        );
        await this.waitPastLoading();
        return;
      } catch {
        if (Date.now() > deadline) {
          throw new Error(`openTenant: tenant "${name}" never became active`);
        }
      }
    }
  }

  /** Navigate via a sidebar link (exact name match, e.g. "Access"). */
  async nav(name: string): Promise<void> {
    await this.page.getByRole("link", { name, exact: true }).first().click();
  }

  /**
   * Open the "Create organization" page via the header tenant switcher
   * (inari-ui src/layout/tenant-switcher.tsx — the item renders only for
   * callers with canCreateOrganizations, LAST in the menu after the org
   * list). The locator wait keeps the permission-gating signal (a missing
   * item fails here); the click is dispatched rather than pointer-driven
   * because the dropdown has no internal scroll — with many orgs the item
   * sits outside the viewport and a pointer click never fires (verified
   * live; UI limitation flagged for inari-ui).
   */
  async openCreateOrganization(): Promise<void> {
    await this.switcherButton().click();
    await this.page
      .getByRole("menuitem", { name: "Create organization" })
      .dispatchEvent("click");
  }

  /**
   * The header tenant-switcher trigger. Its text is the current-context
   * indicator ("All tenants" or the active org's SLUG — the claim-derived
   * label; KC organization names are slugs — optionally with a team
   * suffix) — specs assert it after every switch.
   */
  switcherButton(): Locator {
    return this.page.getByRole("button", { name: "Tenant context switcher" });
  }

  /** The overview page header title (renders the active tenant's slug). */
  overviewTitle(): Locator {
    return this.page.locator(".mono-title").first();
  }

  /**
   * Switch tenant context by org SLUG via the header switcher
   * (inari-ui src/layout/tenant-switcher.tsx — menu items are labeled by
   * the claim-derived org name, which is the slug). Selecting an org
   * triggers a FULL Keycloak re-login (organization scope, prompt:none)
   * that navigates away and lands on /<slug>/overview, so callers MUST poll
   * (helpers/poll.ts) for the new context — never assert immediately. The
   * same org can render twice in the menu (Recent + Organizations
   * sections), hence .first().
   *
   * The click is dispatched, not pointer-driven: the dropdown has no
   * internal scroll, so on a persistent env with many leftover orgs the
   * target item can sit outside the viewport and a pointer click burns the
   * whole actionability wait without ever firing (verified live). The
   * dropdown keyboard path can't address an arbitrary item either. (UI
   * limitation flagged for inari-ui; the switch navigation itself is still
   * fully exercised.)
   */
  async switchTenant(slug: string): Promise<void> {
    await this.switcherButton().click();
    await this.page
      .getByRole("menuitem", { name: slug, exact: true })
      .first()
      .dispatchEvent("click");
  }
}
