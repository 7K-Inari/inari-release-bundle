# k6 load suite (W7 — plan §10, taxonomy §1.3 `load`)

Load scenarios running as k6-operator TestRuns on the persistent e2e
environment, selected by the e2e-stack WorkflowTemplate's **weekly** mode
(`gitops/e2e/argo-workflows/workflowtemplate-e2e-stack.yaml`, `k6-run`
template). **Load tests NEVER run in PR or release gates** (hard rule 6);
threshold violations fail the weekly run (k6 nonzero exit → leg failure →
run Failed → ReportPortal signal) but never block a release.

## Scenarios

| Script | Target | Profile (defaults) | Threshold gates (initial, conservative — calibrate after the first weekly runs) |
|---|---|---|---|
| `permissions-cache.js` | `GET /api/v1/me/permissions` (PEP tenant-cache hot path, inari-server decision 0010) | constant-arrival-rate 50 rps, 10m | `http_req_failed<1%`, `p(95)<500ms`, `checks>99%` |
| `login-storm.js` | Keycloak token endpoint (`/realms/inari/.../token`, direct grant, rotating personas) | ramp 0→30 VUs (2m), hold 8m, down 1m | `http_req_failed<2%`, `p(95)<2s`, `checks>99%` |
| `provisioning-outbox.js` | `POST /api/v1/tenants` write path + read-after-write materialization (the NATS outbox/JetStream relay assertion) | 1 VU, ~20 creates over 5m, 15s pace | `http_req_failed<5%`, create `p(95)<5s`, `tenant_visible_seconds p(95)<10s`, `checks>95%` |

Load-created tenants use janitor-reapable slugs (`e2e-<runid>-load-<n>`) and
are deliberately not deleted by the scenario — decommission is a four-eyes
approval flow owned by the janitor CronWorkflow.

**kubeproxy/tunnel throughput is intentionally NOT covered** (W7 D4):
kubeproxy is disabled on the e2e env (`kubeproxy.enabled: false`) and the
tunnel chain stays docker-only (`scripts/e2e/kubectl/kubectl-tunnel.sh`).

**Login-storm safety:** the e2e realm must keep Keycloak brute-force
detection OFF (the KC default — realm-inari.json carries no securityDefenses
block). Do not enable `bruteForceProtected` on the e2e realm: a lockout
under load breaks every later suite leg that uses the personas.

## Files and layout

Everything in this directory is **flat on purpose**: the k6-run workflow
step mounts the scenario + libs into a ConfigMap (keys cannot contain
slashes), so imports are relative and flat (`./common.js`) — `k6 run` works
identically from a local checkout and inside the runner pod.

- `common.js` — contract env vars (`E2E_*`), KC direct-grant token helper
  (cached per VU, refresh on expiry/401), persona list, janitor-safe slug
  factory, the shared `handleSummary` builder.
- `k6-summary.js` — vendored jslib k6-summary 0.1.0 (MIT) so runner pods
  never egress to jslib.k6.io. Its `jUnit()` emits one plain pass/fail
  `<testcase>` per threshold — no `time` attributes, no `<skipped/>` — which
  the ReportPortal import path tolerates by construction (PRs #145/#147).
  Both the testsuite name and testcase classname are `k6.<scenario>`:
  ReportPortal uses the suite name for its item hierarchy (and ignores the
  classname), so this keeps the three scenarios distinguishable in a launch.
  `handleSummary` also returns the `stdout` key (exporting it suppresses the
  console summary otherwise) and prints the JUnit between `---K6-JUNIT-*---`
  sentinels — the PRIMARY extraction path: the k6-run step reads it from the
  runner pod log (runner pods are already Completed at `stage=finished`, so
  `kubectl cp`/exec cannot work; the cp attempt remains only as a last
  resort for still-Running pathological pods).
- `testrun-template.yaml` — the TestRun manifest rendered by the workflow
  step (envsubst). Never set `spec.cleanup=post` (it would race the
  artifact extraction).

## Running locally

Only the k6 binary is needed (no cluster) — the env is reachable over the
public ingress:

```sh
export E2E_BASE_DOMAIN=inari-e2e.7kgroup.org E2E_KC_URL=https://auth-e2e.7kgroup.org \
  E2E_TENANT_SLUG=e2e-org E2E_RUN_ID=local-$(date +%s) E2E_SCENARIO=permissions-cache \
  E2E_PASSWORD=... E2E_OPERATOR_PASSWORD=... E2E_EDITOR_PASSWORD=... \
  E2E_VIEWER_PASSWORD=... E2E_MEMBER_PASSWORD=...
k6 run --include-system-env-vars permissions-cache.js
```

Shrink the shape with `LOAD_DURATION`/`LOAD_RATE`/`LOAD_VUS`/
`LOAD_PACE_SECONDS` (same knobs the workflow's `load-duration` parameter
sets for acceptance runs). Note `k6 inspect` needs
`--include-system-env-vars` explicitly (unlike `k6 run`).

**Do not run provisioning-outbox locally against the shared env without a
reason** — it creates real tenant orgs (janitor-reaped after the retention
window, but still churn). The read-only scenarios are harmless at small
shape.
