// @p1 @settings — tenant git config write path (e2e plan P1-3).
//
// The kind stack runs with INARI_GIT_PROVIDER=local: golden-path.sh creates a
// host-side bare-repo root (mktemp), bind-mounts it into the kind node at
// /git, and hostPath-mounts it into the server pod at /var/lib/inari/git (the
// provider's INARI_GIT_LOCAL_ROOT). Repos live under that root as
// <org>/<name> — no external git host is involved, so the local provider is
// exercised end-to-end with zero extra provisioning.
//
// As dev-admin in tenant "E2E Org" (slug e2e-org): point the org's git
// config at a NEW unique state repo (e2e-org/e2e-<runId>-*-state) through
// Settings → Git, save, then poll GET /api/v1/tenants/{org}/git-config
// (helpers/api.ts) until the server reports the new value. The config is
// SHARED, so the spec restores the original values through the same UI path
// before it exits (spec independence — see README rule 4).

import { expect, test } from "../../fixtures/failure-dump";

import { uniqueName } from "../../fixtures/org";
import { ApiClient } from "../../helpers/api";
import { TENANT_SLUG } from "../../helpers/env";
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
