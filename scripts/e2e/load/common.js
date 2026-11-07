// Shared helpers for the e2e load scenarios (plan §10 W7; taxonomy §1.3
// `load`). Mounted FLAT next to the scenario scripts by the script ConfigMap
// (ConfigMap keys cannot contain slashes) — imports stay relative and flat so
// `k6 run` works identically from a local checkout and inside the runner pod.
import http from 'k6/http';
import { Counter } from 'k6/metrics';
import { textSummary, jUnit } from './k6-summary.js';

function need(name) {
  const v = __ENV[name];
  if (!v) {
    throw new Error(`missing required env var ${name} (e2e environment contract)`);
  }
  return v;
}

// Contract vars (docs/e2e/environment-contract.md) — the workflow's k6-run
// step injects them into the runner pod (testrun-template.yaml).
export const CFG = {
  baseDomain: need('E2E_BASE_DOMAIN'),
  kcUrl: need('E2E_KC_URL'),
  tenantSlug: need('E2E_TENANT_SLUG'),
  runId: need('E2E_RUN_ID'),
  scenario: need('E2E_SCENARIO'),
};

export function apiBase() {
  return `https://${CFG.baseDomain}/api/v1`;
}

export function tokenUrl() {
  return `${CFG.kcUrl}/realms/inari/protocol/openid-connect/token`;
}

// The seeded personas (scripts/e2e/seed). Passwords arrive as env vars from
// the inari-e2e-personas secret — the runner pod resolves them kubelet-side,
// so no k8s RBAC is involved.
export const PERSONAS = [
  { user: 'dev-admin', password: need('E2E_PASSWORD') },
  { user: 'e2e-operator', password: need('E2E_OPERATOR_PASSWORD') },
  { user: 'e2e-editor', password: need('E2E_EDITOR_PASSWORD') },
  { user: 'e2e-viewer', password: need('E2E_VIEWER_PASSWORD') },
  { user: 'e2e-member', password: need('E2E_MEMBER_PASSWORD') },
];

export const tokenRefreshes = new Counter('token_refreshes');

// directGrant performs ONE login against the Keycloak token endpoint — the
// same direct-grant shape the suites and workflow steps use (public client
// inari-server, org scopes). Tagged endpoint=kc-token so scenario latency
// thresholds can exclude it.
export function directGrant(username, password) {
  const res = http.post(
    tokenUrl(),
    {
      grant_type: 'password',
      client_id: 'inari-server',
      username,
      password,
      scope: 'openid organization:*',
    },
    { tags: { endpoint: 'kc-token' } },
  );
  if (res.status !== 200) {
    return { ok: false, status: res.status, token: '', expiresAt: 0 };
  }
  const body = res.json();
  return {
    ok: !!body.access_token,
    status: res.status,
    token: body.access_token || '',
    // KC default access-token lifespan is 5min; refresh 30s early.
    expiresAt: Date.now() + (body.expires_in || 300) * 1000 - 30000,
  };
}

// bearer returns a cached per-VU dev-admin token, refreshing at expiry.
// Scenarios must additionally retry once on a 401 (a token can be invalidated
// early by a mid-run realm re-import) via invalidateBearer().
let cached = null;
export function bearer() {
  if (!cached || Date.now() > cached.expiresAt) {
    cached = directGrant(PERSONAS[0].user, PERSONAS[0].password);
    tokenRefreshes.add(1);
    if (!cached.ok) {
      throw new Error(`token acquisition failed (HTTP ${cached.status}) — env not seeded?`);
    }
  }
  return cached.token;
}

export function invalidateBearer() {
  cached = null;
}

export function authHeaders(token) {
  return { headers: { Authorization: `Bearer ${token}` }, tags: { endpoint: 'api' } };
}

// slug factory: janitor-reapable tenant slugs (the janitor reaps every e2e-*
// slug except the seeded default e2e-org — workflowtemplate-e2e-janitor.yaml).
export function loadSlug(n) {
  return `e2e-${CFG.runId.slice(0, 8)}-load-${n}`;
}

// makeSummary builds the scenario's handleSummary. The 'stdout' key is
// load-bearing: exporting handleSummary suppresses k6's console summary
// unless one is returned. The JUnit is ALSO printed between sentinels so the
// workflow step has a logs-based extraction fallback when kubectl cp is not
// possible (no tar in the runner image — D6 contingency).
export function makeSummary(scenario) {
  return function handleSummary(data) {
    const junit = jUnit(data, { classname: `k6.${scenario}` });
    return {
      stdout:
        textSummary(data, { indent: ' ', enableColors: false }) +
        '\n---K6-JUNIT-BEGIN---\n' + junit + '\n---K6-JUNIT-END---\n',
      '/tmp/junit.xml': junit,
    };
  };
}
