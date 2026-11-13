# M1W9 LitmusChaos weekly experiments

Five bounded resilience scenarios run inside the existing `e2e-stack` weekly workflow. They use the existing `e2e-env-lock` semaphore, existing persona/artifact secrets, and the existing single-launch ReportPortal importer. There is no ChaosCenter, Grafana, public route, new credential, or PR/release gate.

## Ownership and pins

- **Install:** `7k-app-of-apps/apps/7kgroup/litmuschaos.yaml`, official `litmus-core` chart **3.31.1**, operator/runner **3.31.0**, `operatorMode=namespaced`, destination `inari-e2e`. The GitOps acceptance checks all three CRDs Established and confirms no chart ClusterRole/Binding.
- **Tests:** this directory and `gitops/e2e/argo-workflows/`. Vendored fault definitions come from official `chaos-charts` tag **3.31.0**. The network helper is pinned to `gaiadocker/iproute2@sha256:d0cd5819fbeaf2e7501dcb223bb5bad4fda7c1e76ff9877baba6e559c70b6d1e`.
- **Isolation:** the driver requires exactly namespace `inari-e2e`, domain `inari-e2e.7kgroup.org`, and Keycloak `https://auth-e2e.7kgroup.org`. It resolves exact Ready pod names and renders both `spec.selectors.pods` and `TARGET_PODS`. `chaos-rbac.yaml` contains namespaced Roles only and no Secret access.

## Prerequisites

1. The GitOps Litmus Application is Healthy, its three APIs are served, and deployment `litmus` has one Ready replica.
2. The e2e CNPG cluster has **three healthy instances** before `cnpg-primary-delete`; the driver refuses any other topology.
3. Server replacement and the negative control require `ha=true` and exactly two Ready server replicas.
4. The normal workflow `env-prep` and `seed` have completed. A red baseline `@smoke` prevents injection.
5. k6 must be green in `weekly-suite=all`. `weekly-suite=chaos` deliberately skips k6 for focused manual acceptance.

## Scenarios

All faults self-terminate after 120 seconds. Scenarios are sequential; a failed leg prevents later injection.

| `chaos-scenario` | Fault and exact target | Independent proof |
|---|---|---|
| `cnpg-primary-delete` | Graceful pod-delete of the current CNPG primary | Three-instance preflight; primary changes; all three instances return Ready; a run-owned tenant survives in SQL and through the API |
| `keycloak-latency` | 250 ms server → `keycloak-service.inari-e2e.svc.cluster.local` | Native Keycloak discovery + API 401 probes, gate Playwright selection, recovery smoke |
| `nats-outage` | 100% packet loss on all three NATS pods | Tenant creation is accepted during the outage; at least one unpublished outbox row for that returned `org_id` accumulates; that org drains with no dead-letter and becomes readable after recovery |
| `openfga-latency` | First 250 ms, then 90 seconds server → `openfga.inari-e2e.svc.cluster.local` | Gate Playwright in window one; a fresh run-owned tenant/token in window two must produce 403/5xx or the bounded 75s deadline, never 2xx or stale-token 401; protected read recovers |
| `server-pod-delete` | Graceful delete of exactly one of two server pods | API 401 native probe, gate Playwright, two updated Ready replicas, scenario-org outbox drain, tenant read, recovery smoke |
| `negative-control` | **Manual only:** 100% packet loss on both server pods | Native API probe, direct API check, and Playwright must all observe failure; the Argo leg then fails intentionally after cleanup and recovery |

For the normal gate selection the driver runs Playwright `--grep "@p0|@smoke" --grep-invert @quarantine`. Baseline, NATS, and recovery use `@smoke`. The opt-in `reporter.mjs` marks the first non-setup test; only then does the driver apply the engine. Normal Playwright runs do not load this reporter.

## Manual acceptance

Submit each scenario separately against the reviewed/merged bundle SHA. Wait for each run and require `Succeeded` for the five positive scenarios:

```sh
argo submit --from workflowtemplate/e2e-stack -n inari-e2e \
  -p mode=weekly -p weekly-suite=chaos -p chaos-scenario=<scenario> \
  -p ha=true -p charts-ref=<merged-bundle-sha> \
  -p server-image=edge -p console-image=edge -p agent-image=edge \
  -p server-src-ref=main --wait
```

Run the negative control with `chaos-scenario=negative-control`. **Expected:** the workflow ends `Failed`, `chaos.negative-control` imports as failed into the same ReportPortal launch, and its cleanup/domain-recovery/Playwright-recovery checks are green. If the outage does not break all three required probes, the wrapper says the control was ineffective; that is not validation.

The whole workflow holds `e2e-env-lock` from `resolve` through chaos cleanup and `onExit`. A contending workflow therefore remains pending while a fault is active. Do not run a second chaos submission to “speed up” acceptance.

## JUnit and evidence

Each leg writes:

- `e2e/<workflow-uid>/chaos/<scenario>/junit.xml` — testsuite `chaos.<scenario>`;
- `e2e/<workflow-uid>/chaos/<scenario>/evidence/` — rendered engines/experiments, exact target snapshots, raw ChaosResult JSON, live engine JSON, available runner logs, domain-probe JSON, driver report, and raw Playwright JUnit/HTML/results.

JUnit testcases independently cover baseline, native phase/verdict, native `probeSuccessPercentage`, browser checks, scenario invariants, cleanup/leak checks, and recovery. The score testcase is named `resilience score = <value>%` (prefixed by engine where needed). It is the unchanged Litmus score, not a weighted score. A native 100% cannot hide a failed independent check.

The existing `onExit` handler imports all optional chaos wrappers into the run's **one** ReportPortal launch, including failed/aborted runs, with `weekly-suite` and `chaos-scenario` attributes. Raw Playwright XML remains evidence only, avoiding duplicate ReportPortal items.

## Cleanup and break-glass

Normal cleanup patches each engine to `engineState=stop`, archives results, and deletes only exact run-scoped engine/result/experiment names plus runner/experiment resources recorded in engine status. No broad-label delete is used. A post-run list checks for leftovers.

If the driver pod is lost, the 120-second native duration is the backstop. After identifying the run and confirming namespace `inari-e2e`, use the recorded manifest/evidence names for a bounded stop/delete; never copy these commands against another namespace or substitute a live engine name:

```sh
kubectl -n inari-e2e get chaosengines,chaosresults,pods,jobs \
  -l e2e.inari.io/run-id=<workflow-uid>
kubectl -n inari-e2e patch chaosengine <exact-engine> \
  --type=merge -p '{"spec":{"engineState":"stop"}}'
kubectl -n inari-e2e delete chaosengine <exact-engine> --wait=true --timeout=45s
kubectl -n inari-e2e delete chaosresult <exact-result> --ignore-not-found
kubectl -n inari-e2e delete chaosexperiment <exact-experiment> --ignore-not-found
```

Then run an ordinary `mode=smoke` workflow before further chaos. GitOps installation/topology changes never use direct `kubectl apply`; the commands above are fault cleanup only.

## Activation gates — currently not live

`cronworkflow-weekly.yaml` remains explicitly `weekly-suite: load`. Change it to `all` in a separate activation PR only after **both**:

1. the fuzz-stability successor is merged, at least three consecutive scheduled nightlies are green across env-prep, Ginkgo, Playwright, and fuzz, and the latest scheduled weekly k6 run is green;
2. `e2e-nightly-reset` is enabled and has a successful semaphore-coordinated restore proof.

The reset is a suspended placeholder as of 2026-11-13, so this implementation does **not** claim a live chaos schedule. Activation preserves Saturday 02:17 UTC, `concurrencyPolicy: Forbid`, and the Sunday 01:17 reset spacing. Observe the first scheduled load+chaos run green before calling activation complete. Chaos remains non-gating forever.

## Static tests

```sh
cd scripts/e2e/chaos
go test ./...
node --test reporter.test.mjs
```

CI runs both. The tests cover environment/manifest guards, all engine rendering and pins, native result parsing, JUnit independence, exact-name cleanup, protected-read classification, workflow selection, and the load-only cron.
