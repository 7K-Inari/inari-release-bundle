// Scenario: provisioning-outbox (taxonomy §1.3 `load`; W7 spec §4.3).
// Exercises the tenant provisioning write path: POST /api/v1/tenants, then
// polls until the new org is readable — the read-after-write latency IS the
// NATS outbox/JetStream relay assertion (the stock k6 image has no NATS
// protocol and a custom xk6 image was rejected; API-level materialization
// transitively proves the outbox kept up on the permanently 3-node cluster,
// post-#157).
//
// Tenants are created with janitor-reapable slugs (e2e-<runid>-load-<n>) and
// deliberately NOT deleted here — decommission is a four-eyes approval flow
// owned by the janitor CronWorkflow.
import { check, sleep } from 'k6';
import http from 'k6/http';
import { Trend } from 'k6/metrics';
import { CFG, apiBase, bearer, directGrant, loadSlug, makeSummary, PERSONAS } from './common.js';

// Values are plain seconds (no timeUnit — k6 would otherwise render the
// added seconds as milliseconds in the summary; thresholds compare raw
// values either way).
const visibleSeconds = new Trend('tenant_visible_seconds');

export const options = {
  scenarios: {
    writes: {
      executor: 'constant-vus',
      vus: 1,
      duration: __ENV.LOAD_DURATION || '5m',
    },
  },
  thresholds: {
    // Scoped, not global: the read-back poll's 404s are the protocol
    // (tenant not yet visible), and KC grant calls ride the same metric —
    // a global http_req_failed rate conflates both with real errors (first
    // weekly acceptance tripped at 19% with zero functional check failures).
    'http_req_failed{endpoint:tenant-create}': ['rate<0.05'],
    'http_req_failed{endpoint:kc-token}': ['rate<0.10'],
    'http_req_duration{endpoint:tenant-create}': ['p(95)<5000'],
    tenant_visible_seconds: ['p(95)<10'],
    checks: ['rate>0.95'],
  },
};

// freshToken issues a NEW token per read-back attempt: a token issued before
// the create does not see the new org at all (token-scope staleness,
// live-verified in the upgrade-fixture step).
function freshToken() {
  const grant = directGrant(PERSONAS[0].user, PERSONAS[0].password);
  return grant.ok ? grant.token : bearer();
}

let n = 0;

export default function () {
  const slug = loadSlug(n++);
  const start = Date.now();
  const create = http.post(
    `${apiBase()}/tenants`,
    JSON.stringify({ slug, displayName: `E2E Load ${CFG.runId.slice(0, 8)} ${n}` }),
    {
      headers: { Authorization: `Bearer ${bearer()}`, 'Content-Type': 'application/json' },
      tags: { endpoint: 'tenant-create' },
    },
  );
  // Duplicate slugs are a 500 today (inari-server backlog: 500 vs 409); a
  // rerun of the same run-id must stay idempotent (upgrade-fixture precedent).
  const created = create.status >= 200 && create.status < 300;
  check(create, {
    'create: 2xx or duplicate-slug 500': (r) => (r.status >= 200 && r.status < 300) || r.status === 500,
  });

  const deadline = start + 30000;
  let visible = false;
  while (Date.now() < deadline) {
    const res = http.get(`${apiBase()}/tenants/${slug}`, {
      headers: { Authorization: `Bearer ${freshToken()}` },
      tags: { endpoint: 'tenant-readback' },
      // 404 = not yet materialized — the expected poll outcome, not a
      // failure (keeps http_req_failed scoped to real errors).
      responseCallback: http.expectedStatuses({ min: 200, max: 399 }, 404),
    });
    if (res.status === 200) {
      visible = true;
      break;
    }
    sleep(2);
  }
  if (created) {
    visibleSeconds.add(visible ? (Date.now() - start) / 1000 : 30);
  }
  check(visible, { 'tenant visible within 30s': (v) => v });

  sleep(parseInt(__ENV.LOAD_PACE_SECONDS || '15', 10));
}

export const handleSummary = makeSummary(CFG.scenario);
