// @p1 @settings — tenant git config write path (e2e plan P1-3).
//
// Live e2e environment topology (post kind): inari-server runs with
// INARI_GIT_PROVIDER=local and writes real bare repos (go-git) into the
// shared RWX PVC at <root>/<org>/<name>.git; the git-service Deployment
// serves that PVC over the git protocol (git daemon --export-all,
// ClusterIP :9418 — E2E_GIT_URL). The seed step materializes the tenant's
// default state repo (e2e-org/e2e-org-inari-state) via a platform-resources
// reconcile, so before exercising the UI write path this spec asserts the
// CURRENT config's repo is actually served — the precondition proves
// server -> PVC -> daemon end to end. (Skipped when GIT_URL is unset —
// the legacy kind local loop has no daemon.)
//
// As dev-admin in tenant "E2E Org" (slug e2e-org): point the org's git
// config at a NEW unique state repo (e2e-org/e2e-<runId>-*-state) through
// Settings → Git, save, then poll GET /api/v1/tenants/{org}/git-config
// (helpers/api.ts) until the server reports the new value. The config is
// SHARED, so the spec restores the original values through the same UI path
// before it exits (spec independence — see README rule 4).
//
// The NEW repo's on-disk materialization is deliberately NOT asserted:
// SetGitConfig only upserts the config row (the local provider's EnsureRepo
// runs lazily on the next materializing action), so a ls-remote poll on it
// would be flaky by construction. Repo materialization is the seed's
// write-path proof (scripts/e2e/seed).

import { expect, test } from "../../fixtures/failure-dump";

import { uniqueName } from "../../fixtures/org";
import { ApiClient } from "../../helpers/api";
import { GIT_URL, TENANT_SLUG } from "../../helpers/env";
import { lsRemote } from "../../helpers/git";
import { poll } from "../../helpers/poll";
import { AppShellPage } from "../../pages/app-shell.page";
import { GitConfigSettingsPage } from "../../pages/settings/git-config.page";

const NEW_REPO = `${TENANT_SLUG}/${uniqueName("state")}`;

interface TenantGitConfig {
  repo: string;
  baseBranch?: string;
  commitPolicy?: string;
}

test("git config write path persists via the local provider @p1 @settings", async ({
  page,
}) => {
  test.setTimeout(120_000);

  const api = await ApiClient.forPersona();
  const readConfig = () =>
    api
      .get<{ config?: TenantGitConfig }>(`/api/v1/tenants/${TENANT_SLUG}/git-config`)
      .then((b) => b.config);
  const original = await readConfig();
  expect(original?.repo, "current git config resolvable via API").toBeTruthy();

  // Precondition (live env): the tenant's configured state repo is served
  // by the in-cluster git service — the git write path is alive before the
  // UI mutates the config. ls-remote resolves with [] for a ref-less repo,
  // so the assertion is reachability, not content.
  if (GIT_URL) {
    await poll(
      async () => {
        const refs = await lsRemote(`${GIT_URL}/${original!.repo}.git`);
        expect(Array.isArray(refs)).toBe(true);
      },
      { timeout: 60_000, interval: 5_000, message: "git service serves the tenant state repo" },
    );
  }

  // Boot the console as dev-admin and land on Settings → Git.
  const shell = new AppShellPage(page);
  await page.goto(`/${TENANT_SLUG}/settings/org/git`, { waitUntil: "domcontentloaded" });
  await shell.waitForBoot();
  await shell.waitPastLoading();
  const git = new GitConfigSettingsPage(page);

  // --- mutate through the UI ---
  await poll(async () => expect(git.repoInput()).toHaveValue(original!.repo), {
    timeout: 20_000,
    interval: 1_000,
    message: "git form loaded with current repo",
  });
  await git.setRepo(NEW_REPO);
  await git.save();
  await poll(async () => expect(git.savedMessage()).toBeVisible(), {
    timeout: 20_000,
    interval: 1_000,
    message: "git config save confirmation",
  });

  // --- persisted (settings writes may propagate asynchronously — poll) ---
  await poll(
    async () => {
      const cfg = await readConfig();
      expect(cfg?.repo).toBe(NEW_REPO);
    },
    { timeout: 30_000, interval: 2_000, message: "git config persisted via API" },
  );

  // --- restore the original config through the same UI path (shared org) ---
  await git.setRepo(original!.repo);
  await git.save();
  // The second save hides and re-shows the saved card (component resets
  // `saved` on submit) and disables the button while the PUT is in flight,
  // so require the card visible AND the button re-enabled.
  await poll(
    async () => {
      await expect(git.savedMessage()).toBeVisible();
      await expect(git.saveButton()).toBeEnabled();
    },
    { timeout: 20_000, interval: 1_000, message: "git config restore confirmation" },
  );
  await poll(
    async () => {
      const cfg = await readConfig();
      expect(cfg?.repo).toBe(original!.repo);
    },
    { timeout: 30_000, interval: 2_000, message: "git config restored via API" },
  );
});
