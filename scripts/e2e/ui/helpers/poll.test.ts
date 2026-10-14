// Contract tests for poll() — the 2026-10-14 root cause: bare-expect
// callbacks (resolve undefined on success) must count as success per the
// docstring, or every such site burns the full timeout and fails with the
// expected state present.

import assert from "node:assert/strict";
import { describe, it } from "node:test";

import { poll } from "./poll.ts";

const fast = { timeout: 200, interval: 10 } as const;

describe("poll", () => {
  it("returns the truthy value", async () => {
    const v = await poll(async () => 42, fast);
    assert.equal(v, 42);
  });

  it("void fn resolving without throwing = success (bare expect pattern)", async () => {
    let calls = 0;
    const v = await poll(async () => {
      calls++;
      // bare expect(): resolves undefined
    }, fast);
    assert.equal(v, undefined);
    assert.equal(calls, 1, "must not burn the full timeout");
  });

  it("keeps polling while the fn throws, then succeeds", async () => {
    let calls = 0;
    const v = await poll(async () => {
      calls++;
      if (calls < 3) throw new Error("not yet");
      return true;
    }, fast);
    assert.equal(v, true);
    assert.equal(calls, 3);
  });

  it("explicit false keeps polling (falsy-but-defined is not success)", async () => {
    let calls = 0;
    await assert.rejects(
      poll(async () => {
        calls++;
        return false;
      }, fast),
      /timed out/,
    );
    assert.ok(calls > 2);
  });

  it("timeout error carries the last thrown error as cause", async () => {
    await assert.rejects(
      poll(async () => {
        throw new Error("the real reason");
      }, fast),
      (e: Error) => e.message.includes("the real reason") && e.message.includes("timed out"),
    );
  });
});
