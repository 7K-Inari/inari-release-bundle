// Settings → Git page object (Org defaults card: tenant state repo + commit
// policy). Markup from inari-ui
// src/pages/settings/org/git/org-defaults-card.tsx — the current Git
// settings page is the GitOps repo registry; the tenant git-config write
// path lives on the "Org defaults" card. No assertions.

import type { Locator, Page } from "@playwright/test";

export class GitConfigSettingsPage {
  static readonly LABEL = "Git";
  static readonly SAVED_TEXT = "Defaults saved.";

  constructor(readonly page: Page) {}

  /** Transient success text rendered after a successful PUT. */
  savedMessage(): Locator {
    return this.page.getByText(GitConfigSettingsPage.SAVED_TEXT, { exact: true });
  }

  /** Error text rendered when the PUT fails. */
  errorMessage(): Locator {
    return this.page.locator(".u-danger").first();
  }

  /** The state-repo input (card field "Scaffold git org", #org-scaffold). */
  repoInput(): Locator {
    return this.page.getByLabel("Scaffold git org", { exact: true });
  }

  /** Commit policy select (#org-policy). */
  commitPolicySelect(): Locator {
    return this.page.getByLabel("Commit policy default", { exact: true });
  }

  saveButton(): Locator {
    return this.page.getByRole("button", { name: "Save Defaults", exact: true });
  }

  async setRepo(repo: string): Promise<void> {
    await this.repoInput().fill(repo);
  }

  async setCommitPolicy(policy: "direct" | "pull_request"): Promise<void> {
    await this.commitPolicySelect().selectOption(policy);
  }

  async save(): Promise<void> {
    await this.saveButton().click();
  }
}
