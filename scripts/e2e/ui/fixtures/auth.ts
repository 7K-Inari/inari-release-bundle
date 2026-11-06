// Persona registry + login helper for the Playwright setup-project pattern.
//
// Each persona logs in once per run through the REAL Keycloak 26 two-step UI
// flow (username → submit → password) and saves its storageState to
// .auth/<persona>.json; test projects reuse the file via `storageState` in
// playwright.config.ts. Personas are provisioned by the seed (scripts/e2e/seed)
// (see the persona table + no-mutation rules in scripts/e2e/ui/README.md);
// the chromium project still runs as dev-admin — waves owning persona-scoped
// specs add matching projects.

import type { Page } from "@playwright/test";

import { DEV_ADMIN, PERSONA_PASSWORDS } from "../helpers/env";
import { LoginPage } from "../pages/login.page";

export interface Persona {
  /** storageState file stem under .auth/ (e.g. "dev-admin"). */
  name: string;
  username: string;
  password: string;
}

export const PERSONAS: Record<string, Persona> = {
  // Provisioned by the seed step (scripts/e2e/seed): platform-admins group,
  // E2E Org creator. Credentials from E2E_USER/E2E_PASSWORD on the live env.
  devAdmin: {
    name: "dev-admin",
    username: DEV_ADMIN.username,
    password: DEV_ADMIN.password,
  },
  // Provisioned by the seed step (one per ADR-0013 built-in role, plus the
  // role-less propagation target). Passwords from the E2E_*_PASSWORD contract
  // variables (Vault-backed on the live env; username defaults locally).
  // Specs must never mutate the role assignments of operator/editor/viewer;
  // member is owned by the role-lifecycle spec and must be restored to
  // role-less.
  operator: { name: "e2e-operator", username: "e2e-operator", password: PERSONA_PASSWORDS["e2e-operator"] },
  editor: { name: "e2e-editor", username: "e2e-editor", password: PERSONA_PASSWORDS["e2e-editor"] },
  viewer: { name: "e2e-viewer", username: "e2e-viewer", password: PERSONA_PASSWORDS["e2e-viewer"] },
  member: { name: "e2e-member", username: "e2e-member", password: PERSONA_PASSWORDS["e2e-member"] },
};

export function authFile(persona: Persona): string {
  return `.auth/${persona.name}.json`;
}

/**
 * Drive a fresh page through the console's OIDC redirect and the KC two-step
 * login, then persist storageState for `persona`.
 */
export async function loginAsPersona(page: Page, persona: Persona): Promise<void> {
  const login = new LoginPage(page);
  await login.gotoViaApp();
  await login.login(persona.username, persona.password);
  // The SPA still shows its "Loading…" gate while it fetches /me/* with the
  // fresh token; storageState is only useful once the session is fully
  // established, so wait for boot before saving. Require non-empty text:
  // right after the OIDC redirect the body is still empty (""), which would
  // trivially pass a plain !== "Loading…" check before the SPA has even
  // processed the callback and persisted its tokens.
  await page.waitForFunction(
    () => document.body.innerText.trim().length > 0 && !document.body.innerText.includes("Loading…"),
    { timeout: 45_000 },
  );
  await page.context().storageState({ path: authFile(persona) });
}
