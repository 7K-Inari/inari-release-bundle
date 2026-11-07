// Scenario: login-storm (taxonomy §1.3 `load`; W7 spec §4.2).
// Ramps direct-grant logins against the Keycloak token endpoint, rotating the
// five seeded personas. Gates on failure rate + p95 login latency.
//
// SAFETY (W7 D8): the e2e realm must keep Keycloak brute-force detection OFF
// (realm-inari.json carries no securityDefenses block — the KC default). Do
// NOT enable bruteForceProtected on the e2e realm: a lockout under load
// breaks every later suite leg that uses the personas.
import { check } from 'k6';
import { CFG, PERSONAS, directGrant, makeSummary } from './common.js';

export const options = {
  scenarios: {
    storm: {
      executor: 'ramping-vus',
      startVUs: 0,
      stages: [
        { duration: __ENV.LOAD_RAMP_UP || '2m', target: parseInt(__ENV.LOAD_VUS || '30', 10) },
        { duration: __ENV.LOAD_DURATION || '8m', target: parseInt(__ENV.LOAD_VUS || '30', 10) },
        { duration: __ENV.LOAD_RAMP_DOWN || '1m', target: 0 },
      ],
      gracefulRampDown: '30s',
    },
  },
  thresholds: {
    http_req_failed: ['rate<0.02'],
    'http_req_duration{endpoint:kc-token}': ['p(95)<2000'],
    checks: ['rate>0.99'],
  },
};

export default function () {
  const persona = PERSONAS[(__VU + __ITER) % PERSONAS.length];
  const grant = directGrant(persona.user, persona.password);
  check(grant, {
    'login: 200': (g) => g.status === 200,
    'login: access_token issued': (g) => g.ok,
  });
}

export const handleSummary = makeSummary(CFG.scenario);
