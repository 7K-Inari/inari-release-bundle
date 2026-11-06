// Create-organization page object (/create-organization). Field ids, error
// surfaces, and the submit-button label swap ("Create organization" →
// "Creating…") come from inari-ui src/pages/organizations/create-organization.tsx.
// No assertions.

import type { Locator, Page } from "@playwright/test";

export class CreateOrganizationPage {
  static readonly HEADING = "Create organization";

  constructor(readonly page: Page) {}

  /** h1 of the page — doubles as the "form rendered" marker. */
  heading(): Locator {
    return this.page.getByRole("heading", {
      name: CreateOrganizationPage.HEADING,
      exact: true,
    });
  }

  nameInput(): Locator {
    return this.page.locator("#org-name");
  }

  slugInput(): Locator {
    return this.page.locator("#org-slug");
  }

  /**
   * Submit button in its idle state. While a create is in flight the label
   * flips to "Creating…" and the button is disabled, so this locator being
   * enabled again doubles as the "submission settled" signal.
   */
  submitButton(): Locator {
    return this.page.getByRole("button", {
      name: CreateOrganizationPage.HEADING,
      exact: true,
    });
  }

  /** Field-level slug error (client-side pattern check + server 409). */
  slugError(): Locator {
    return this.page.locator("form p.text-xs.u-danger");
  }

  /** Form-level error for non-409 submission failures. */
  submitError(): Locator {
    return this.page.locator("form p.text-sm.u-danger");
  }

  /** Fill the form and submit. Does not wait for navigation. */
  async createOrganization(displayName: string, slug: string): Promise<void> {
    await this.nameInput().fill(displayName);
    await this.slugInput().fill(slug);
    await this.submitButton().click();
  }
}
