// Setup project: authenticate every persona via the real Keycloak login and
// save .auth/<persona>.json. Personas are seeded by scripts/e2e/seed;
// the chromium project consumes dev-admin — persona-scoped test projects are
// added by the waves that own those specs.

import { test as setup } from "@playwright/test";

import { loginAsPersona, PERSONAS } from "./auth";

for (const persona of Object.values(PERSONAS)) {
  setup(`authenticate ${persona.name}`, async ({ page }) => {
    await loginAsPersona(page, persona);
  });
}
