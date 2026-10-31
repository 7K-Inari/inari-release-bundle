# Chainsaw smoke tests — platform stack verification

Declarative replacement for the retired `scripts/lib/verify.sh`. These tests
assert **cluster state only** — provisioning and orchestration (ArgoCD
Application waits, kind creation, backup restore) stay in bash (hard rule
from the e2e migration plan).

## What is covered

`bootstrap/` (label `e2e.inari.io/set=smoke`, tier `p0`) — the smoke set,
parity with the old `verify_stack`:

| Test | Assertion | Replaces |
|---|---|---|
| `cnpg-ready` | CNPG `Cluster/postgresql` phase `Cluster in healthy state`, all instances ready | `wait_cnpg_cluster` |
| `workloads-ready` | StatefulSets `keycloak`/`nats` ready, Deployments `nats-box`/`openfga` available | `kubectl rollout status` waits |
| `keycloak-realm` | `Keycloak/keycloak` + `KeycloakRealmImport/inari-realm-inari` Ready, **plus** HTTP probe of `/realms/inari` (CR readiness ≠ realm served) | `verify_keycloak_realm` |
| `databases-exist` | databases `inari`/`keycloak`/`openfga` exist (psql on the CNPG primary) | `verify_postgres` |
| `openfga-health` | `http://openfga:8080/healthz` from an ephemeral curl pod | `verify_openfga` |
| `nats-jetstream` | `/jsz` on the nats-0 pod IP (monitor port is pod-local) via nats-box | `verify_nats` |
| `agent-compat` | ConfigMap `inari-agent-compat` carries the declared `supportedRange`/`recommended` | new (plan §6) |

`dr/` (label `e2e.inari.io/set=dr`) — post-restore verification, run by
`dr-drill.sh` **in addition to** the smoke set. Proves restored *content*
landed (realm row in the keycloak DB, public tables in the inari DB, OpenFGA
store `inari`) — a fresh bootstrap is healthy without any restore, so the
smoke set alone cannot detect a no-op restore. Expects a backup taken from a
fully deployed stack (inari-server running).

The `dr` set is deliberately **not** in the `smoke` set: PostSync hooks must
never select it (it would fail on a fresh install where nothing was
restored).

## Usage

Called automatically by `scripts/bootstrap.sh` (smoke set) and
`scripts/dr-drill.sh` (smoke + dr sets) via `verify_chainsaw` in
`scripts/lib/common.sh`. Standalone against an existing dev stack:

```sh
chainsaw test --config scripts/e2e/chainsaw/.chainsaw.yaml \
  --test-dir scripts/e2e/chainsaw/bootstrap \
  --values scripts/e2e/chainsaw/values/expected.yaml
```

- Namespace defaults to `inari` (config); override with `--namespace <ns>`
  (the bash wrappers pass `--namespace "$NAMESPACE"`).
- Select a subset by label, e.g. the PostSync-hook smoke selection:
  `--selector e2e.inari.io/set=smoke`.
- JUnit XML (one testcase per Test, for ReportPortal per plan §3.4) is
  enabled in `.chainsaw.yaml` (`reportFormat: XML`, report name `junit`).
  The bash wrappers write it to `scripts/e2e/chainsaw/reports/junit.xml`
  (gitignored; the wrapper creates the dir); override with
  `E2E_CHAINSAW_REPORT_DIR`. Standalone: omit `--report-path` (writes
  `./junit.xml`) or `mkdir -p` the target dir first — chainsaw does not
  create it. The future Argo workflow step will point it at
  `/artifacts/chainsaw/`.

## Installing chainsaw

Pinned: **v0.2.x** (developed/validated against v0.2.13).

```sh
# prebuilt binary (note: v0.2.x does not compile with Go ≥1.26 — `go
# install` fails on the testing.MainStart interface; use the release binary)
curl -fsSL https://github.com/kyverno/chainsaw/releases/download/v0.2.13/chainsaw_linux_amd64.tar.gz | tar xz chainsaw
```

`bootstrap.sh` fails fast via `require chainsaw` if it is missing.

## Expected-values coupling (single sync point)

`values/expected.yaml` mirrors chart/gitops defaults and **must be bumped in
the same PR** that changes any of:

| values/expected.yaml | Source of truth |
|---|---|
| `agent.supportedRange` / `agent.recommended` | `charts/inari-platform/values.yaml` `agent.*` |
| `postgresql.instances` | `charts/inari-platform/values.yaml` `postgresql.instances` / gitops `platform-config` values |
| `keycloak.instances` | `charts/inari-platform/values.yaml` `keycloak.instances` |
| `nats.replicas` | `gitops/platform/nats.yaml` `config.cluster.replicas` |
| `openfga.replicas` | `gitops/platform/openfga.yaml` `replicaCount` |

This coupling is deliberate: it enforces the declared-range honesty that
AGENTS.md requires for `agent.*`.
