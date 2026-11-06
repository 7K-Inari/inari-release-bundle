// Contract tests for helpers/git.ts — git ls-remote works on local paths
// too, so no daemon is needed: init a bare repo in a tmpdir for the success
// cases and point at a missing path for the failure mode.

import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { after, describe, it } from "node:test";

import { lsRemote } from "./git.ts";

const dir = mkdtempSync(`${tmpdir()}/e2e-git-test-`);
after(() => rmSync(dir, { recursive: true, force: true }));

describe("lsRemote", () => {
  it("resolves with an empty ref list for a fresh bare repo", async () => {
    const repo = `${dir}/empty.git`;
    execFileSync("git", ["init", "--bare", "--initial-branch=main", repo]);
    const refs = await lsRemote(repo);
    assert.deepEqual(refs, []);
  });

  it("resolves with the ref list once a commit exists", async () => {
    const repo = `${dir}/with-commit.git`;
    const work = `${dir}/work`;
    execFileSync("git", ["init", "--bare", "--initial-branch=main", repo]);
    execFileSync("git", ["init", "-q", "--initial-branch=main", work]);
    execFileSync("git", ["-C", work, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "--allow-empty", "-m", "init"]);
    execFileSync("git", ["-C", work, "push", "-q", repo, "main"]);
    const refs = await lsRemote(repo);
    assert.ok(refs.some((l) => l.endsWith("refs/heads/main")), `expected main ref, got ${refs}`);
  });

  it("rejects for a path that is not a repo", async () => {
    await assert.rejects(lsRemote(join(dir, "does-not-exist.git")));
  });
});
