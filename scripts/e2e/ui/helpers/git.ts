// Minimal git-daemon probe for specs/settings/git-config.spec.ts:
// `git ls-remote` against the in-cluster git service (E2E_GIT_URL).
//
// On the e2e environment inari-server runs with INARI_GIT_PROVIDER=local:
// its go-git provider writes REAL bare repos into the shared RWX PVC at
// <root>/<org>/<name>.git, and the git-service Deployment serves that PVC
// over the git protocol (git daemon --export-all). A successful ls-remote
// proves the whole path — server -> PVC -> daemon. Note a freshly
// initialized repo has no refs yet: success = the command exits 0, NOT
// non-empty output.
//
// Requires the `git` binary on the runner (Playwright container images ship
// it; the Argo suite-step image contract lists it).

import { execFile } from "node:child_process";
import { promisify } from "node:util";

const execFileAsync = promisify(execFile);

/**
 * Advertised refs ("<sha>\t<ref>" lines) of a repo URL; rejects with stderr
 * when the repo is not served. An empty array means the repo exists but has
 * no commits yet.
 */
export async function lsRemote(repoURL: string): Promise<string[]> {
  const { stdout } = await execFileAsync("git", ["ls-remote", repoURL], { timeout: 30_000 });
  return stdout.split("\n").filter((l) => l.length > 0);
}
