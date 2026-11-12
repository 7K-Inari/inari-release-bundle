# scripts/e2e/fuzz — Schemathesis API fuzz (W8)

Property-based API testing against the **deployed** inari-server, using the
official Schemathesis 4.33.4 image. The workflow runs only in `mode=nightly`,
after both Ginkgo and Playwright have finished (successfully or not), and never
blocks a PR or release. It hunts HTTP 5xx responses and response-schema
violations and reports them as JUnit failures in the run's ReportPortal launch.

## Spec source and run contract

The init container pulls:

```
ghcr.io/7k-inari/inari-server-openapi:<server-image>  →  /spec/dist/openapi.yaml
```

The tag is exactly the workflow's deployed `server-image` (`edge` → `edge`, a
release `X.Y.Z` → `X.Y.Z`). A missing tag or `dist/openapi.yaml` fails the leg;
there is deliberately no fallback to `edge`, which could test the wrong
contract. Nightly runs use unsigned edge images with `cosign-verify=false`, so
this leg does not add a separate spec-signature check; its integrity anchors
are GHCR TLS and exact deployed-tag parity. The schema paths already include
`/api/v1`, so `base-url` in `schemathesis.toml` is only
`https://${E2E_BASE_DOMAIN}`.

`run.sh` obtains a dev-admin direct-grant token with Python stdlib, ensures the
run-owned tenant once, then invokes `st run`. Only network-level failures are
retried; an HTTP 500 is never retried into a pass.

## Shared-environment safety

- **One tenant:** `E2E_FUZZ_ORG=e2e-<workflow-uid>-fuzz`. `ensure_tenant.py`
  creates it once (or reuses it on a local rerun) and every `{org}` path value
  is forced to that slug. Real write operations therefore exercise an owned
  fixture, not the seeded `e2e-org`.
- **No repeated create:** `createTenant` is excluded from the generated loop.
  Repeating one forced slug found a real duplicate-create 500 tracked by
  **7K-Inari/inari-server#216**; the one-time setup avoids making that known
  server defect a nightly source of artificial duplicates.
- **No DELETE:** a single `include-method = "DELETE"`, `enabled = false` block
  disables every DELETE operation. Coverage's unexpected-method probes are
  also disabled as defense in depth.
- **Personas/RBAC protected:** POST/PUT/PATCH under members, teams, roles, and
  RBAC paths are disabled with one operation-name regex; reads remain covered.
  Multiple `include-*` keys are not used in a disabled block because
  Schemathesis ORs them (a broad-exclusion regression is covered by tests).
- **No global sync:** `POST /api/v1/admin/catalog/sync` is disabled.
- **No response-driven targeting:** response values are not reused for later
  requests, so real resource IDs cannot redirect writes into seeded state.
- **Bounded residue:** the run-owned org matches the janitor's
  `e2e-<runId>-*` prefix and the nightly reset bounds all remaining residue to
  less than 24 hours.

The fuzz leg is serialized after the functional suites to avoid random writes
racing their assertions. Its `depends` expression deliberately runs it even
when a sibling suite fails.

## Checks and budget

Only these checks are enabled:

- `not_a_server_error` — any 5xx except a deliberate 501 is a failure;
- `response_schema_conformance` — a documented response must match its schema.

HTTP 501 is allowed only because several e2e features are intentionally
configured off and return `Not Implemented` with a deliberate detail message;
all other 5xx responses remain failures.

`status_code_conformance` is initially off: Huma does not document every
intentional 4xx, so undocumented-but-valid client rejections must not become
nightly flakes. Revisit only after nightly signal shows it is actionable.

The checked-in profile uses one worker, 25 examples per operation per fuzzing
round, 10 requests/second, a 10-second request timeout, at most 25 failures,
and a 20-minute total Schemathesis budget. The Argo template has a separate
40-minute deadline for schema loading, setup, reporting, and artifact upload.

The first live acceptance run exercised 136 operations and filed genuine
findings as inari-server **#216** (duplicate tenant create),
**#217** (nullable identity-provider schema), **#218** (duplicate cluster-set
name), and **#219** (invalid input escaping as 500). Those failures are not
suppressed or baselined away; only deliberate feature-disabled 501 responses
are accepted as environment variance.

## Artifacts and replay

The leg uploads:

| Artifact | Purpose |
|---|---|
| `e2e/<uid>/fuzz/junit.xml` | `fuzz` testsuite; the exit-handler's normal import-copy normalization handles skips/empty time attributes |
| `e2e/<uid>/fuzz/report/run.json` | machine-readable verdict, counts, warnings, and seed |
| `e2e/<uid>/fuzz/report/cassette.yaml` | VCR request/response cassette |
| `e2e/<uid>/fuzz/report/cache/` | crash files for `st replay` |
| `e2e/<uid>/fuzz/report/openapi.yaml` | exact tested schema |
| `e2e/<uid>/fuzz/report/schemathesis.toml` | exact checked-in configuration |

Authorization headers and sensitive keys are sanitized. **Response bodies are
written verbatim by Schemathesis**, so treat the report directory as sensitive
e2e data even on the persistent non-production environment.

With the report downloaded into `./report` and the live-env variables exported,
replay one archived crash with the same pinned image:

```sh
cd scripts/e2e/fuzz
export E2E_FUZZ_ORG=e2e-replay-fuzz  # do not target a seeded org

docker run --rm --network host \
  -v "$PWD:/work/fuzz:ro" -v "$PWD/report:/report:ro" \
  -e E2E_BASE_DOMAIN -e E2E_KC_URL -e E2E_USER -e E2E_PASSWORD \
  -e E2E_FUZZ_ORG \
  schemathesis/schemathesis:4.33.4@sha256:708d934c8208a8f785bdf1b1610476e691f1efda0b7a3cb9ce7536d0f3280eec \
  sh -c 'E2E_TOKEN=$(python3 /work/fuzz/fetch_token.py) && export E2E_TOKEN && st --config-file /work/fuzz/schemathesis.toml replay /report/cache/crashes/<crash-id>.json --schema-location /report/openapi.yaml'
```

Use the VCR cassette to inspect the complete request chain when a crash file
needs more context.

## Local verification

Run the stdlib contract tests (Python 3.11+) from the repository root:

```sh
python3 -m unittest discover -s scripts/e2e/fuzz -p 'test_*.py' -v
sh -n scripts/e2e/fuzz/run.sh
```

For a live run, use a unique slug and the same image. The temporary host
directories are writable by the image's UID 1000:

```sh
cd scripts/e2e/fuzz
export E2E_BASE_DOMAIN=inari-e2e.7kgroup.org
export E2E_KC_URL=https://auth-e2e.7kgroup.org
export E2E_USER=dev-admin E2E_PASSWORD=…
export E2E_FUZZ_ORG="e2e-$(date +%s)-fuzz"

install -d -m 0777 /tmp/inari-fuzz/spec /tmp/inari-fuzz/artifacts
oras pull -o /tmp/inari-fuzz/spec \
  ghcr.io/7k-inari/inari-server-openapi:edge

docker run --rm --network host \
  -v /tmp/inari-fuzz/spec:/spec:ro \
  -v /tmp/inari-fuzz/artifacts:/artifacts \
  -v "$PWD:/work/fuzz:ro" \
  -e E2E_BASE_DOMAIN -e E2E_KC_URL -e E2E_USER -e E2E_PASSWORD -e E2E_FUZZ_ORG \
  schemathesis/schemathesis:4.33.4@sha256:708d934c8208a8f785bdf1b1610476e691f1efda0b7a3cb9ce7536d0f3280eec \
  sh /work/fuzz/run.sh /spec/dist/openapi.yaml
```

The runner preserves reports and returns Schemathesis's actual verdict; a zero
exit without JUnit is also a failed leg.
