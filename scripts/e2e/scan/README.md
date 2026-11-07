# scripts/e2e/scan — Harbor/Trivy scan-gate driver (W6)

The release-mode image vulnerability gate of the `e2e-stack` WorkflowTemplate
(`gitops/e2e/argo-workflows/workflowtemplate-e2e-stack.yaml`, leg
`harbor-scan-gate`; architecture plan C9 / §5 step 2). Release runs only —
`when: mode == release`, parallel with `cosign-verify`, blocking `env-prep`
(fail fast and cheap).

For each candidate image (`SERVER_IMAGE` / `CONSOLE_IMAGE` / `AGENT_IMAGE` in
the `ghcr-proxy` proxy-cache project on `harbor.7kgroup.org`):

1. **Resolve** the tag through the proxy to its scannable digests. Multi-arch
   indexes are scanned **per platform sub-manifest** (attestations skipped —
   they carry no packages): Harbor's proxy registers pushed sub-manifests
   promptly, while index/tag registration is asynchronous (waits on all
   children, up to ~7 min, trims or drops on timeout — verified against
   Harbor v2.15.4 source and live) — far too racy for a gate. The CVE content
   of an index is the union of its platform manifests, so per-digest scanning
   is the same signal with deterministic timing. Harbor's core service
   reverse-proxies `/v2/` and hosts `/service/token`: the driver fetches a
   registry token (robot basic auth) and pulls manifests with it.
2. **Register** each digest's artifact: pull the sub-manifest by digest
   through the proxy (kicks Harbor's background push), kick its blobs with
   1-byte range GETs (shortens the server-side dependency wait), poll the
   artifact API until registered.
3. **Trigger** the scan explicitly (`POST
   .../artifacts/<digest>/scan`) and poll `?with_scan_overview=true` until
   `scan_status=Success` — asynchronous; a prior scan is never trusted
   (scan-on-push latency races). `Error`/`Stopped` gets exactly one
   retrigger. Platforms scan concurrently per image.
4. **Archive** the full `additions/vulnerabilities` report per platform
   (`REPORTS_DIR`, uploaded to S3 at `e2e/<uid>/scan/reports/`).
5. **Fail** when the aggregate Critical count exceeds
   `SCAN_CRITICAL_THRESHOLD` (default 0) and always write the synthetic
   **`security` JUnit** (`OUT_DIR/junit.xml`) — including configuration
   errors, so a failed gate still lands in the run's ReportPortal launch.

## Configuration (env)

| Variable | Default | Meaning |
|---|---|---|
| `HARBOR_API` | `http://harbor-core.harbor.svc.cluster.local/api/v2.0` | Harbor API base — in-cluster service DNS by default: the release gate never depends on the public gateway/DNS/TLS path (exit-handler `RP_ENDPOINT` precedent). Overrides exist for out-of-cluster debugging. |
| `HARBOR_REGISTRY` | `http://harbor-core.harbor.svc.cluster.local` | `/v2/` + `/service/token` endpoint (priming pull; core proxies to the registry) |
| `HARBOR_PROJECT` | `ghcr-proxy` | proxy-cache project |
| `HARBOR_ROBOT_USERNAME` / `HARBOR_ROBOT_TOKEN` | — (required) | robot account (ESO secret `harbor-scanner`; missing = loud config failure, never a silent skip) |
| `SCAN_CRITICAL_THRESHOLD` | `0` | fail when Critical count exceeds this |
| `SCAN_RUN_ID` | — | workflow uid (S3 report pointer in JUnit failures) |
| `OUT_DIR` / `REPORTS_DIR` | `/artifacts/out` / `/artifacts/reports` | artifact paths |
| `SCAN_POLL_INTERVAL_SECONDS` / `SCAN_TIMEOUT_SECONDS` | `15` / `900` | poll tuning (per image) |

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
