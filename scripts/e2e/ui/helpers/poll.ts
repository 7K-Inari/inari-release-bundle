// Polling helper with explicit timeout/interval.
//
// RULE (see README.md): no spec ever asserts immediately after a mutation.
// All propagation — OrgTeamSync interval, /me/permissions cache, PEP TTL —
// is observed via poll() until the expected state shows up or the timeout
// expires.

export interface PollOptions {
  /** Total time to keep trying, in ms. */
  timeout: number;
  /** Delay between attempts, in ms. */
  interval: number;
  /** Optional label for the timeout error message. */
  message?: string;
}

/**
 * Re-invoke `fn` until it returns a truthy value (or resolves without
 * throwing, when T is void) or `timeout` elapses. Returns the last value.
 * Throws the last error (or a timeout error) when the deadline passes.
 */
export async function poll<T>(
  fn: () => Promise<T>,
  { timeout, interval, message }: PollOptions,
): Promise<T> {
  const deadline = Date.now() + timeout;
  let lastError: unknown;
  let lastValue: T | undefined;
  for (;;) {
    try {
      lastValue = await fn();
      // Truthy value: caller signalled success. `undefined` from a void fn
      // (e.g. `async () => expect(locator).toBeVisible()`): resolved without
      // throwing, so the assertion passed — success per the docstring.
      // Other falsy values (false/0/""/null) mean "keep polling".
      if (lastValue) return lastValue;
      if (lastValue === undefined) return undefined as T;
    } catch (err) {
      lastError = err;
    }
    if (Date.now() >= deadline) break;
    await new Promise((resolve) => setTimeout(resolve, interval));
  }
  const what = message ? ` (${message})` : "";
  if (lastError instanceof Error) {
    throw new Error(`poll: timed out after ${timeout}ms${what}: ${lastError.message}`, {
      cause: lastError,
    });
  }
  throw new Error(`poll: timed out after ${timeout}ms${what}`);
}
