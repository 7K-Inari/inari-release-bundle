// Unique organization-name factory for specs that create orgs:
//   e2e-<runId>-<seq>
// runId comes from the env contract (helpers/env.ts RUN_ID: E2E_RUN_ID on
// the workflow — also the janitor's reap prefix — GITHUB_RUN_ID on the
// legacy GHA path, random hex locally). seq is a monotonic counter per
// worker process.

import { RUN_ID } from "../helpers/env";

let seq = 0;

export function uniqueOrgName(): string {
  seq += 1;
  return `e2e-${RUN_ID}-${seq}`;
}

/** Unique DNS-1123-safe object name with a semantic suffix (roles, teams). */
export function uniqueName(kind: string): string {
  seq += 1;
  return `e2e-${RUN_ID}-${seq}-${kind}`;
}
