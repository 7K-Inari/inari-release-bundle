// Scenario: permissions-cache (taxonomy §1.3 `load`; W7 spec §4.1).
// Hammers GET /api/v1/me/permissions — the PEP tenant-cache hot path
// (inari-server decision 0010) — and gates on error rate + p95 latency.
// Uses e2e-viewer's fixed e2e-org membership, NOT dev-admin: the outbox leg
// creates tenants as dev-admin, whose permissions cost/response grows with
// those retained memberships and would make repeated runs self-poisoning.
// Initial thresholds are deliberately conservative (W7 D7); calibrate after
// the first weekly runs. Load shape is env-tunable (LOAD_RATE/LOAD_DURATION)
// so acceptance runs can shrink it.
import { check } from 'k6';
import http from 'k6/http';
import { CFG, apiBase, bearer, invalidateBearer, makeSummary } from './common.js';

export const options = {
  scenarios: {
    cache: {
      executor: 'constant-arrival-rate',
      rate: parseInt(__ENV.LOAD_RATE || '50', 10),
      timeUnit: '1s',
      duration: __ENV.LOAD_DURATION || '10m',
      preAllocatedVUs: 10,
      maxVUs: 50,
    },
  },
  thresholds: {
    http_req_failed: ['rate<0.01'],
    'http_req_duration{endpoint:me-permissions}': ['p(95)<500'],
    checks: ['rate>0.99'],
  },
};

export default function () {
  let res = http.get(`${apiBase()}/me/permissions`, {
    headers: { Authorization: `Bearer ${bearer('e2e-viewer')}` },
    tags: { endpoint: 'me-permissions' },
  });
  if (res.status === 401) {
    // Token invalidated mid-run (e.g. realm re-import) — refresh once.
    invalidateBearer();
    res = http.get(`${apiBase()}/me/permissions`, {
      headers: { Authorization: `Bearer ${bearer('e2e-viewer')}` },
      tags: { endpoint: 'me-permissions' },
    });
  }
  check(res, {
    'permissions: 200': (r) => r.status === 200,
    'permissions: JSON body': (r) => {
      try {
        return typeof r.json() === 'object';
      } catch {
        return false;
      }
    },
  });
}

export const handleSummary = makeSummary(CFG.scenario);
