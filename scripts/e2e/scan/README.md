# scripts/e2e/scan — Harbor/Trivy scan-gate driver (W6)

The release-mode image vulnerability gate of the `e2e-stack` WorkflowTemplate
(`gitops/e2e/argo-workflows/workflowtemplate-e2e-stack.yaml`, leg
`harbor-scan-gate`; architecture plan C9 / §5 step 2). Release runs only —
`when: mode == release`, parallel with `cosign-verify`, blocking `env-prep`
(fail fast and cheap).

For each candidate image (`SERVER_IMAGE` / `CONSOLE_IMAGE` / `AGENT_IMAGE` in
the `ghcr-proxy` proxy-cache project on `harbor.7kgroup.org`):

1. **Prime** the proxy cache with a manifest-only pull (a proxy-cache
   artifact exists only after a pull).
2. **Trigger** the scan explicitly (`POST
   /api/v2.0/projects/<p>/repositories/<r>/artifacts/<tag>/scan`) — a prior
   scan is never trusted; scan-on-push latency races the poll otherwise.
3. **Poll** `GET .../artifacts/<tag>?with_scan_overview=true` until
   `scan_status=Success` (async; `Error`/`Stopped` get one retrigger).
4. **Archive** the full `additions/vulnerabilities` report per image
   (`REPORTS_DIR`, uploaded to S3 at `e2e/<uid>/scan/reports/`).
5. **Fail** when the Critical count exceeds `SCAN_CRITICAL_THRESHOLD`
   (default 0) and always write the synthetic **`security` JUnit**
   (`OUT_DIR/junit.xml`) — including configuration errors, so a failed gate
   still lands in the run's ReportPortal launch.

## Configuration (env)

| Variable | Default | Meaning |
|---|---|---|
| `HARBOR_API` | `https://harbor.7kgroup.org/api/v2.0` | Harbor API base |
| `HARBOR_REGISTRY` | `https://harbor.7kgroup.org` | registry endpoint (priming pull) |
| `HARBOR_PROJECT` | `ghcr-proxy` | proxy-cache project |
| `HARBOR_ROBOT_USERNAME` / `HARBOR_ROBOT_TOKEN` | — (required) | robot account (ESO secret `harbor-scanner`; missing = loud config failure, never a silent skip) |
| `SCAN_CRITICAL_THRESHOLD` | `0` | fail when Critical count exceeds this |
| `SCAN_RUN_ID` | — | workflow uid (S3 report pointer in JUnit failures) |
| `OUT_DIR` / `REPORTS_DIR` | `/artifacts/out` / `/artifacts/reports` | artifact paths |
| `SCAN_POLL_INTERVAL_SECONDS` / `SCAN_TIMEOUT_SECONDS` | `15` / `600` | poll tuning (per image) |

## Rules

- **CVE allowlists are managed in Harbor, never here** — the gate counts
  Harbor's own severity summary so Harbor-side allowlisting stays the single
  exemption knob.
- **Pull-blocking stays OFF** on `ghcr-proxy` (hard rule 7) — enforcement
  lives in this gate, never in the pull path.
- **Complements cosign, never replaces it** — cosign proves provenance, this
  gate proves content.
- The robot token never appears in logs or errors.

## Tests

```
go test ./...
```

`httptest`-driven fake Harbor: poll state machine (incl. retrigger-once),
404 re-priming, threshold semantics, summary-vs-fallback counting, JUnit
shape, the API's double-URL-encoded repository paths, token non-leakage, and
`run()`'s configuration-error paths.

Runbook (owner prerequisites: robot account, Vault entry, scan-on-push,
daily re-scan): [`gitops/e2e/README.md` §Harbor scan gate](../../../gitops/e2e/README.md).
