// Shared env contract for the UI e2e suite — the single process.env reader
// (docs/e2e/environment-contract.md invariant 7; fixtures/specs import from
// here, never from process.env). Two resolution modes:
//
//   LIVE mode (E2E_BASE_DOMAIN set): the §3.2 contract against the
//   persistent e2e environment. Real ingress gives a single origin —
//   console at https://$E2E_BASE_DOMAIN, API at /api/v1 on the same host,
//   and one Keycloak URL serving both the browser redirect and direct-grant
//   token calls (the kind-era issuer split is gone). E2E_SEEDED must be
//   "true" — a run against an unseeded env is a setup bug, not a test
//   failure (invariant 4), so the module throws at import (Playwright
//   config load) instead of burning browser timeouts.
//
//   LEGACY mode (E2E_BASE_DOMAIN unset): the kind local-dev inner loop
//   (port-forwards + the former ui-proxy). Byte-compatible with the
//   pre-migration defaults so the local loop keeps working; CI never uses
//   this mode.

import { randomBytes } from "node:crypto";

const LIVE_DOMAIN = process.env.E2E_BASE_DOMAIN || "";
const LIVE = LIVE_DOMAIN !== "";

function fail(msg: string): never {
  throw new Error(`e2e env: ${msg}`);
}

if (LIVE) {
  if (!process.env.E2E_KC_URL) {
    fail("E2E_BASE_DOMAIN is set (live mode) but E2E_KC_URL is missing");
  }
  if (process.env.E2E_SEEDED !== "true") {
    fail(
      "E2E_SEEDED is not \"true\" — the seed step (scripts/e2e/seed) must run " +
        "after env-prep and before any suite leg; running against an unseeded " +
        "env is a setup bug, not a test failure (environment-contract invariant 4)",
    );
  }
}

/** Console entry URL. Live: the single-origin base. Legacy: the ui-proxy. */
export const UI_BASE = LIVE ? `https://${LIVE_DOMAIN}` : process.env.UI_BASE || "http://127.0.0.1:8080";

/** Base URL for direct-grant tokens / KC admin calls (non-browser). */
export const KC_URL = LIVE ? process.env.E2E_KC_URL! : process.env.KC_URL || `http://${process.env.KC_HOST || "127.0.0.1:18091"}`;

/** host[:port] the browser is redirected to for the Keycloak login. */
export const KC_HOST = LIVE ? new URL(KC_URL).host : process.env.KC_HOST || "127.0.0.1:18091";

/** Keycloak realm. */
export const KC_REALM = process.env.KC_REALM || "inari";

/** inari-server API base (single-origin in live mode; overridable for local debugging). */
export const API_BASE = process.env.API_BASE || (LIVE ? `${UI_BASE}/api/v1` : UI_BASE);

/** Tenant card display name the smoke spec clicks. */
export const TENANT_NAME = LIVE
  ? process.env.E2E_TENANT_NAME || "E2E Org"
  : process.env.TENANT_NAME || "E2E Org";

/** Tenant slug used in API paths. */
export const TENANT_SLUG = LIVE ? process.env.E2E_TENANT_SLUG || "e2e-org" : process.env.TENANT || "e2e-org";

/**
 * Base URL of the in-cluster git service (git://…:9418), live mode only;
 * "" disables the git-daemon precondition in specs/settings/git-config.spec.ts.
 */
export const GIT_URL = LIVE ? process.env.E2E_GIT_URL || "" : "";

/**
 * Run identity feeding the uniqueName() factories: E2E_RUN_ID on the
 * workflow (Argo UID; also the janitor's e2e-<runId>-* reap prefix),
 * GITHUB_RUN_ID on the legacy GHA path, random hex locally.
 */
export const RUN_ID =
  process.env.E2E_RUN_ID || process.env.GITHUB_RUN_ID || randomBytes(4).toString("hex");

/** The dev-admin persona (suite default identity; seeded by the seed step). */
export const DEV_ADMIN = {
  username: process.env.E2E_USER || "dev-admin",
  password: process.env.E2E_PASSWORD || "dev-admin",
};

/**
 * Passwords for the four seeded role personas. On the live env they come
 * from the Vault-backed inari-e2e-personas secret (mounted by the workflow
 * as these env vars); the username defaults preserve kind-era parity for the
 * local loop.
 */
export const PERSONA_PASSWORDS: Record<string, string> = {
  "e2e-operator": process.env.E2E_OPERATOR_PASSWORD || "e2e-operator",
  "e2e-editor": process.env.E2E_EDITOR_PASSWORD || "e2e-editor",
  "e2e-viewer": process.env.E2E_VIEWER_PASSWORD || "e2e-viewer",
  "e2e-member": process.env.E2E_MEMBER_PASSWORD || "e2e-member",
};
