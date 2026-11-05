// Contract tests for helpers/env.ts — the §3.2 environment contract
// (docs/e2e/environment-contract.md) plus the legacy kind-mode defaults.
//
// env.ts resolves at IMPORT time, so each case cache-busts the module URL
// and mutates process.env between imports. Env is snapshot/restored per
// test so cases never leak into each other.

import assert from "node:assert/strict";
import { afterEach, describe, it } from "node:test";

const ENV_KEYS = [
  "E2E_BASE_DOMAIN",
  "E2E_KC_URL",
  "E2E_SEEDED",
  "E2E_TENANT_NAME",
  "E2E_TENANT_SLUG",
  "E2E_GIT_URL",
  "E2E_RUN_ID",
  "E2E_USER",
  "E2E_PASSWORD",
  "E2E_OPERATOR_PASSWORD",
  "E2E_EDITOR_PASSWORD",
  "E2E_VIEWER_PASSWORD",
  "E2E_MEMBER_PASSWORD",
  "UI_BASE",
  "KC_HOST",
  "KC_URL",
  "KC_REALM",
  "API_BASE",
  "TENANT_NAME",
  "TENANT",
  "GITHUB_RUN_ID",
] as const;

let saved: Record<string, string | undefined> = {};

function clearEnv() {
  saved = {};
  for (const k of ENV_KEYS) {
    saved[k] = process.env[k];
    delete process.env[k];
  }
}

afterEach(() => {
  for (const k of ENV_KEYS) {
    if (saved[k] === undefined) delete process.env[k];
    else process.env[k] = saved[k];
  }
});

let seq = 0;
function importEnv() {
  // Query suffix defeats the ESM module cache so resolution re-runs.
  return import(`./env.ts?case=${seq++}`);
}

function liveEnv(extra: Record<string, string> = {}) {
  Object.assign(process.env, {
    E2E_BASE_DOMAIN: "inari-e2e.example.com",
    E2E_KC_URL: "https://auth-e2e.example.com",
    E2E_SEEDED: "true",
    ...extra,
  });
}

describe("env — legacy mode (E2E_BASE_DOMAIN unset, kind local-dev)", () => {
  it("keeps the kind-era defaults byte-compatible", async () => {
    clearEnv();
    const env = await importEnv();
    assert.equal(env.UI_BASE, "http://127.0.0.1:8080");
    assert.equal(env.KC_HOST, "127.0.0.1:18091");
    assert.equal(env.KC_URL, "http://127.0.0.1:18091");
    assert.equal(env.KC_REALM, "inari");
    assert.equal(env.API_BASE, "http://127.0.0.1:8080");
    assert.equal(env.TENANT_NAME, "E2E Org");
    assert.equal(env.TENANT_SLUG, "e2e-org");
    assert.equal(env.GIT_URL, "");
  });

  it("honors the legacy variable names", async () => {
    clearEnv();
    Object.assign(process.env, {
      UI_BASE: "http://127.0.0.1:9999",
      KC_HOST: "10.0.0.1:1234",
      TENANT_NAME: "Custom Org",
      TENANT: "custom-org",
    });
    const env = await importEnv();
    assert.equal(env.UI_BASE, "http://127.0.0.1:9999");
    assert.equal(env.KC_URL, "http://10.0.0.1:1234");
    assert.equal(env.API_BASE, "http://127.0.0.1:9999");
    assert.equal(env.TENANT_NAME, "Custom Org");
    assert.equal(env.TENANT_SLUG, "custom-org");
  });

  it("does not require E2E_SEEDED (kind bring-up and seed are one local flow)", async () => {
    clearEnv();
    await importEnv(); // must not throw
  });

  it("persona passwords default to username (kind-era seed parity)", async () => {
    clearEnv();
    const env = await importEnv();
    assert.equal(env.DEV_ADMIN.username, "dev-admin");
    assert.equal(env.DEV_ADMIN.password, "dev-admin");
    assert.equal(env.PERSONA_PASSWORDS["e2e-operator"], "e2e-operator");
    assert.equal(env.PERSONA_PASSWORDS["e2e-member"], "e2e-member");
  });

  it("RUN_ID falls back to GITHUB_RUN_ID, then a random hex", async () => {
    clearEnv();
    process.env.GITHUB_RUN_ID = "12345";
    let env = await importEnv();
    assert.equal(env.RUN_ID, "12345");
    clearEnv();
    env = await importEnv();
    assert.match(env.RUN_ID, /^[0-9a-f]{8}$/);
  });
});

describe("env — live mode (E2E_BASE_DOMAIN set, §3.2 contract)", () => {
  it("derives the single-origin URLs from E2E_BASE_DOMAIN", async () => {
    clearEnv();
    liveEnv();
    const env = await importEnv();
    assert.equal(env.UI_BASE, "https://inari-e2e.example.com");
    assert.equal(env.API_BASE, "https://inari-e2e.example.com/api/v1");
    assert.equal(env.KC_URL, "https://auth-e2e.example.com");
    assert.equal(env.KC_HOST, "auth-e2e.example.com");
    assert.equal(env.KC_REALM, "inari");
  });

  it("reads the tenant + git-service contract variables", async () => {
    clearEnv();
    liveEnv({
      E2E_TENANT_NAME: "Contract Org",
      E2E_TENANT_SLUG: "contract-org",
      E2E_GIT_URL: "git://git-service.inari-e2e.svc:9418",
      E2E_RUN_ID: "run-abc",
      GITHUB_RUN_ID: "ignored",
    });
    const env = await importEnv();
    assert.equal(env.TENANT_NAME, "Contract Org");
    assert.equal(env.TENANT_SLUG, "contract-org");
    assert.equal(env.GIT_URL, "git://git-service.inari-e2e.svc:9418");
    assert.equal(env.RUN_ID, "run-abc");
  });

  it("keeps API_BASE overridable for local debugging", async () => {
    clearEnv();
    liveEnv({ API_BASE: "http://127.0.0.1:18090/api/v1" });
    const env = await importEnv();
    assert.equal(env.API_BASE, "http://127.0.0.1:18090/api/v1");
  });

  it("fails fast when E2E_SEEDED is unset (contract invariant 4)", async () => {
    clearEnv();
    liveEnv();
    delete process.env.E2E_SEEDED;
    await assert.rejects(importEnv(), /E2E_SEEDED/);
  });

  it("fails fast when E2E_SEEDED is not 'true'", async () => {
    clearEnv();
    liveEnv({ E2E_SEEDED: "false" });
    await assert.rejects(importEnv(), /E2E_SEEDED/);
  });

  it("fails fast when E2E_KC_URL is missing", async () => {
    clearEnv();
    liveEnv();
    delete process.env.E2E_KC_URL;
    await assert.rejects(importEnv(), /E2E_KC_URL/);
  });

  it("reads persona passwords from the E2E_*_PASSWORD contract variables", async () => {
    clearEnv();
    liveEnv({
      E2E_USER: "dev-admin",
      E2E_PASSWORD: "s3cret-admin",
      E2E_OPERATOR_PASSWORD: "s3cret-op",
      E2E_EDITOR_PASSWORD: "s3cret-ed",
      E2E_VIEWER_PASSWORD: "s3cret-vw",
      E2E_MEMBER_PASSWORD: "s3cret-mb",
    });
    const env = await importEnv();
    assert.equal(env.DEV_ADMIN.password, "s3cret-admin");
    assert.equal(env.PERSONA_PASSWORDS["e2e-operator"], "s3cret-op");
    assert.equal(env.PERSONA_PASSWORDS["e2e-editor"], "s3cret-ed");
    assert.equal(env.PERSONA_PASSWORDS["e2e-viewer"], "s3cret-vw");
    assert.equal(env.PERSONA_PASSWORDS["e2e-member"], "s3cret-mb");
  });
});
