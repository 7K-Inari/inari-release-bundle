#!/usr/bin/env node
// Persona seed for the console UI e2e (Playwright) suite — IDEMPOTENT.
//
// Runs as one release-e2e workflow step between stack bring-up (golden-path)
// and the Playwright run, and is safe to re-run against a kept
// (KEEP_CLUSTER=true) kind cluster. It owns four things:
//
//   1. The inari-ui client redirect whitelist (absorbed from the former
//      inline workflow shell block): the realm re-import wipes the
//      browser-origin redirectUris/webOrigins every run, so they are
//      re-merged here. Do not re-add this to the workflow or the suite.
//   2. The four e2e personas as Keycloak users (enabled, verified,
//      non-temporary password = username), each joined to the e2e-org
//      Keycloak Organization so the `organization:*` scope / org claim works.
//   3. Role assignment THROUGH THE INARI-SERVER API
//      (PUT /api/v1/tenants/{org}/members/{subject} {roleId}) — never KC
//      groups, never direct DB — so seeding exercises the real membership
//      path. A broken roles/members API fails this script loudly: that is a
//      P0-class signal, not a seed bug.
//   4. A default git-config for e2e-org (PUT /tenants/{org}/git-config) —
//      the server leaves it unset (404) until the first write and there is
//      no DELETE, while the git-config spec saves and restores the ORIGINAL
//      repo. Seeded once, idempotently (see ensureGitConfig).
//
// Personas (one per ADR-0013 built-in role; bundles verified in inari-server
// migration 0029_roles.sql):
//
//   user          password      role       purpose
//   dev-admin     dev-admin     admin      EXISTS (stack suite seed.go: platform-admins group,
//                                          E2E Org creator) — never touched here beyond
//                                          being the API caller
//   e2e-operator  e2e-operator  operator   Platform Engineer
//   e2e-editor    e2e-editor    editor     Developer
//   e2e-viewer    e2e-viewer    viewer     read-only RBAC surface
//   e2e-member    e2e-member    (none)     org member with NO role/team — propagation
//                                          target for the role-lifecycle spec
//
// RULES FOR SPEC AUTHORS (also in scripts/e2e/ui/README.md):
//   - Specs must NEVER mutate the role assignments of e2e-operator /
//     e2e-editor / e2e-viewer, and must never touch dev-admin.
//   - e2e-member is the only persona whose role may change; the
//     role-lifecycle spec owns it and MUST restore it to role-less
//     (afterEach/afterAll). This script detects a leaked role on e2e-member
//     and warns, but never silently "repairs" spec state.
//   - KC users are NOT added to platform-admins (or any KC group).
//   - Spec-created objects (orgs/teams/roles) go through the unique-name
//     factory (fixtures/org.ts); personas are fixed names owned by this seed.
//
// Env (names/defaults aligned with scripts/e2e/ui/helpers/env.ts):
//   KC_URL        Keycloak base for admin/token calls (default http://127.0.0.1:18091)
//   KC_REALM      realm (default inari)
//   API_URL       inari-server API base (default http://127.0.0.1:18090/api/v1)
//   NAMESPACE     k8s namespace holding the inari-keycloak-admin secret (default inari)
//   TENANT        tenant slug (default e2e-org)
//   SEED_ADMIN_USER / SEED_ADMIN_PASS  API caller (default $E2E_USER/$E2E_PASSWORD,
//                 then dev-admin/dev-admin)
//   UI_ORIGIN     browser origin to whitelist on inari-ui (default http://127.0.0.1:8080)
//
// Pure decision logic is exported for node:test (seed-personas.test.mjs);
// the side-effecting main() only runs when invoked directly.

import { execFileSync } from "node:child_process";
import { pathToFileURL } from "node:url";

const KC_URL = process.env.KC_URL || "http://127.0.0.1:18091";
const KC_REALM = process.env.KC_REALM || "inari";
const API_URL = process.env.API_URL || "http://127.0.0.1:18090/api/v1";
const NAMESPACE = process.env.NAMESPACE || "inari";
const TENANT = process.env.TENANT || "e2e-org";
const UI_ORIGIN = process.env.UI_ORIGIN || "http://127.0.0.1:8080";
const ADMIN_USER = process.env.SEED_ADMIN_USER || process.env.E2E_USER || "dev-admin";
const ADMIN_PASS = process.env.SEED_ADMIN_PASS || process.env.E2E_PASSWORD || "dev-admin";

export const PERSONAS = [
  { username: "e2e-operator", password: "e2e-operator", role: "operator" },
  { username: "e2e-editor", password: "e2e-editor", role: "editor" },
  { username: "e2e-viewer", password: "e2e-viewer", role: "viewer" },
  { username: "e2e-member", password: "e2e-member", role: null },
];

const BUILTIN_ROLES = ["admin", "operator", "editor", "viewer"];

function die(message) {
  console.error(`seed-personas: FAIL: ${message}`);
  process.exit(1);
}

function log(message) {
  console.log(`seed-personas: ${message}`);
}

// --- pure decision logic (unit-tested) --------------------------------------

/**
 * Merge the browser origin into a KC client representation. Returns
 * { body, changed } where body is the full client object to PUT back.
 */
export function mergeClientRedirects(client, origin) {
  const redirectUris = [...(client.redirectUris || [])];
  const webOrigins = [...(client.webOrigins || [])];
  let changed = false;
  if (!redirectUris.includes(`${origin}/*`)) {
    redirectUris.push(`${origin}/*`);
    changed = true;
  }
  if (!webOrigins.includes(origin)) {
    webOrigins.push(origin);
    changed = true;
  }
  return { body: { ...client, redirectUris, webOrigins }, changed };
}

/**
 * Map built-in role names to their ids from GET /tenants/{org}/roles.
 * Throws (fail fast) when any built-in is missing — a broken roles endpoint
 * or bundle is a P0-class signal, not a seed bug.
 */
export function requireBuiltinRoles(roles) {
  const byName = {};
  for (const name of BUILTIN_ROLES) {
    const role = (roles || []).find((r) => r.name === name && r.builtin);
    if (!role) {
      throw new Error(
        `built-in role "${name}" missing from GET /tenants/{org}/roles ` +
          `(got: ${(roles || []).map((r) => r.name).join(", ") || "none"}) — membership path is broken`,
      );
    }
    byName[name] = role.id;
  }
  return byName;
}

/**
 * Decide which role assignments are still needed. `userIds` maps persona
 * username -> KC user id; `members` is GET /tenants/{org}/members output.
 * Returns { assignments: [{username, userId, role}], skipped: [username],
 * memberLeak: [roles] | null }.
 */
export function planRoleAssignments(personas, userIds, members) {
  const byUserId = new Map((members || []).map((m) => [m.userId, m.roles || []]));
  const assignments = [];
  const skipped = [];
  let memberLeak = null;
  for (const p of personas) {
    const held = byUserId.get(userIds[p.username]) || [];
    if (p.role === null) {
      if (held.length > 0) memberLeak = held;
      continue;
    }
    if (held.includes(p.role)) {
      skipped.push(p.username);
    } else {
      assignments.push({ username: p.username, userId: userIds[p.username], role: p.role });
    }
  }
  return { assignments, skipped, memberLeak };
}

// --- side-effecting helpers --------------------------------------------------

async function api(method, url, { token, body } = {}) {
  const res = await fetch(url, {
    method,
    headers: {
      ...(token ? { authorization: `Bearer ${token}` } : {}),
      ...(body !== undefined ? { "content-type": "application/json" } : {}),
    },
    body: body !== undefined ? JSON.stringify(body) : undefined,
  });
  if (!res.ok) {
    throw Object.assign(new Error(`${method} ${url} -> ${res.status}: ${await res.text()}`), {
      status: res.status,
    });
  }
  // Several OK responses carry no body (KC POST /users answers 201-empty,
  // PUTs answer 204): res.json() would throw "Unexpected end of JSON input".
  const text = await res.text();
  return text === "" ? null : JSON.parse(text);
}

function kcAdminToken() {
  // Same credential the former inline workflow block used: the
  // inari-platform-admin client credentials from the inari-keycloak-admin
  // secret (client id is fixed; only the secret is cluster-generated).
  const b64 = execFileSync(
    "kubectl",
    ["-n", NAMESPACE, "get", "secret", "inari-keycloak-admin", "-o", "jsonpath={.data.client-secret}"],
    { encoding: "utf8" },
  ).trim();
  if (!b64) die(`secret inari-keycloak-admin not found in namespace ${NAMESPACE} (stack not up?)`);
  return clientCredentialsToken("inari-platform-admin", Buffer.from(b64, "base64").toString("utf8"));
}

async function clientCredentialsToken(clientId, clientSecret) {
  const res = await fetch(`${KC_URL}/realms/${KC_REALM}/protocol/openid-connect/token`, {
    method: "POST",
    headers: { "content-type": "application/x-www-form-urlencoded" },
    body: new URLSearchParams({
      grant_type: "client_credentials",
      client_id: clientId,
      client_secret: clientSecret,
    }),
  });
  if (!res.ok) die(`KC admin token request failed: ${res.status} ${await res.text()}`);
  return (await res.json()).access_token;
}

async function userToken() {
  const res = await fetch(`${KC_URL}/realms/${KC_REALM}/protocol/openid-connect/token`, {
    method: "POST",
    headers: { "content-type": "application/x-www-form-urlencoded" },
    body: new URLSearchParams({
      grant_type: "password",
      client_id: "inari-server",
      username: ADMIN_USER,
      password: ADMIN_PASS,
      scope: "openid organization:*",
    }),
  });
  if (!res.ok) die(`user token request as ${ADMIN_USER} failed: ${res.status} ${await res.text()}`);
  return (await res.json()).access_token;
}

async function ensureClientRedirects(at) {
  const clients = await api("GET", `${KC_URL}/admin/realms/${KC_REALM}/clients?clientId=inari-ui`, { token: at });
  const client = clients[0];
  if (!client) die("inari-ui client not found (realm import did not run?)");
  const { body, changed } = mergeClientRedirects(client, UI_ORIGIN);
  if (!changed) {
    log("inari-ui redirect whitelist already present — skipped");
    return;
  }
  await api("PUT", `${KC_URL}/admin/realms/${KC_REALM}/clients/${client.id}`, { token: at, body });
  log(`inari-ui: whitelisted ${UI_ORIGIN} (redirectUris + webOrigins)`);
}

async function ensureUser(at, { username, password }) {
  const kc = (m, p, opts) => api(m, `${KC_URL}/admin/realms/${KC_REALM}${p}`, { token: at, ...opts });
  let users = await kc("GET", `/users?username=${encodeURIComponent(username)}&exact=true`);
  let id = users[0]?.id;
  if (!id) {
    await kc("POST", "/users", {
      body: {
        username,
        enabled: true,
        email: `${username}@inari.local`,
        emailVerified: true,
        credentials: [{ type: "password", value: password, temporary: false }],
      },
    });
    users = await kc("GET", `/users?username=${encodeURIComponent(username)}&exact=true`);
    id = users[0]?.id;
    if (!id) die(`created KC user ${username} but cannot read it back`);
    log(`created KC user ${username}`);
  } else {
    log(`KC user ${username} exists — skipped create`);
  }
  // KC 26.x: create alone can leave the account unverified / with required
  // actions — force the final state (same lesson as the golden-path seeding).
  // Never touches groups: personas must NOT join platform-admins.
  await kc("PUT", `/users/${id}`, {
    body: { emailVerified: true, requiredActions: [], enabled: true },
  });
  return id;
}

async function joinOrganization(at, userId, username) {
  const orgs = await api("GET", `${KC_URL}/admin/realms/${KC_REALM}/organizations?search=${encodeURIComponent(TENANT)}&exact=true`, { token: at });
  const org = orgs.find((o) => o.name === TENANT || o.alias === TENANT) || orgs[0];
  if (!org) die(`KC organization for tenant "${TENANT}" not found (golden-path did not create the tenant?)`);
  // KC 26.3 removed PUT .../members/{userId} (405): membership is now
  // POST .../members with the user ID as a JSON string body (201), and a
  // duplicate add answers 409 — treat that as success (was: idempotent PUT).
  try {
    await api("POST", `${KC_URL}/admin/realms/${KC_REALM}/organizations/${org.id}/members`, { token: at, body: userId });
  } catch (err) {
    if (err.status !== 409) throw err;
  }
  log(`${username} joined KC organization ${TENANT}`);
  return org.id;
}

async function ensureRoleAssignments(token, userIds) {
  const apiCall = (m, p, opts) => api(m, `${API_URL}${p}`, { token, ...opts });
  let roles;
  try {
    roles = (await apiCall("GET", `/tenants/${TENANT}/roles`)).roles;
  } catch (err) {
    die(`GET /tenants/${TENANT}/roles failed — ${err.message}`);
  }
  let roleIds;
  try {
    roleIds = requireBuiltinRoles(roles);
  } catch (err) {
    die(err.message);
  }

  // Bounded poll: PUT returns 404 until the server's users projection has
  // observed the KC org join (OrgTeamSync interval). Any other error fails
  // fast — a broken membership API is a P0-class signal, not a seed bug.
  const deadline = Date.now() + 120_000;
  const pending = new Map(PERSONAS.filter((p) => p.role).map((p) => [p.username, p]));
  while (Date.now() < deadline) {
    const members = (await apiCall("GET", `/tenants/${TENANT}/members`)).members;
    const plan = planRoleAssignments(PERSONAS, userIds, members);
    for (const s of plan.skipped) {
      if (pending.delete(s)) log(`${s}: role already assigned — skipped`);
    }
    if (plan.memberLeak) {
      log(`WARNING: e2e-member holds roles [${plan.memberLeak}] — a spec leaked a role assignment; the role-lifecycle spec must restore e2e-member to role-less`);
    }
    for (const a of plan.assignments) {
      try {
        await apiCall("PUT", `/tenants/${TENANT}/members/${a.userId}`, { body: { roleId: roleIds[a.role] } });
        pending.delete(a.username);
        log(`${a.username}: assigned role ${a.role} via PUT /tenants/${TENANT}/members/${a.userId}`);
      } catch (err) {
        if (err.status === 404) continue; // projection lag — retry until deadline
        die(`PUT /tenants/${TENANT}/members/${a.userId} (${a.username}, role ${a.role}) failed — ${err.message}`);
      }
    }
    if (pending.size === 0) return;
    await new Promise((r) => setTimeout(r, 5000));
  }
  die(`role assignments still pending after 120s: ${[...pending.keys()].join(", ")} (users projection not observing KC org joins?)`);
}

// The Settings git-config spec (@p1 @settings) restores the tenant's
// ORIGINAL state repo after exercising the write path, so e2e-org needs a
// config to exist before the suite runs. The server does not create one on
// tenant creation (GET /git-config -> 404 "git config not set" until the
// first PUT) and there is no DELETE, so seed a stable default once —
// idempotent, skipped when a config is already present.
async function ensureGitConfig(token) {
  const url = `${API_URL}/tenants/${TENANT}/git-config`;
  const res = await fetch(url, { headers: { authorization: `Bearer ${token}` } });
  if (res.ok) {
    log(`git-config for ${TENANT} already set — skipped`);
    return;
  }
  if (res.status !== 404) die(`GET ${url} -> ${res.status}: ${await res.text()}`);
  await api("PUT", url, {
    token,
    body: { repo: `${TENANT}/${TENANT}-inari-state`, commitPolicy: "direct" },
  });
  log(`git-config for ${TENANT}: seeded ${TENANT}/${TENANT}-inari-state (direct)`);
}

async function main() {
  log(`seeding personas against KC=${KC_URL} API=${API_URL} tenant=${TENANT}`);
  const at = await kcAdminToken();
  await ensureClientRedirects(at);

  const userIds = {};
  for (const p of PERSONAS) {
    userIds[p.username] = await ensureUser(at, p);
  }
  for (const p of PERSONAS) {
    await joinOrganization(at, userIds[p.username], p.username);
  }

  const ut = await userToken();
  await ensureRoleAssignments(ut, userIds);
  await ensureGitConfig(ut);
  log("OK — all personas seeded");
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  main().catch((err) => die(err.message));
}
