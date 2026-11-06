# scripts/e2e/seed — persona seed + handoff publisher (workflow step)

Idempotent seed for the e2e environment, running as a workflow step between
**env-prep** and the suite legs (env contract §3.5 /
[docs/e2e/environment-contract.md](../../../docs/e2e/environment-contract.md)
§1.1). Replaces the retired `scripts/e2e/ui/seed/seed-personas.mjs`.

## What it owns

1. **The `inari-ui` redirect whitelist** (`https://$E2E_BASE_DOMAIN/*`) — the
   realm re-import wipes it on every candidate deploy; re-merged here (hard
   rule §8.4). Never add this to a workflow or a suite.
2. **The realm-scope repair** (GAP markers, `scopes.go`): the realm import's
   explicit `clientScopes` array suppresses Keycloak's built-in scopes —
   without `basic` + `organization` attached to `inari-server` and
   `inari-ui`, every token request dies with `invalid_scope` (verified live
   2026-11-05). The dev-admin token probe (`sub` + `aud=inari-server`) is the
   tripwire that fails the run in seconds when the repair regresses.
3. **The five fixed personas** as Keycloak users, each joined to the
   tenant's KC organization. Passwords converge from the `E2E_*_PASSWORD`
   contract variables (the Vault-backed `inari-e2e-personas` secret on the
   live env) — a Vault rotation propagates on the next seed run.
   Plus **one non-persona platform-ops identity** (`e2e-ops`,
   `E2E_OPS_USER`/`E2E_OPS_PASSWORD`, secret key `ops-password`): a
   `platform-admins` member the janitor's approve step authenticates as —
   inari-server forbids a requester deciding their own decommission request
   (four-eyes), and the suite personas are deliberately not platform-admins.
   It gets NO role assignments and NO suite consumes it.
4. **Role assignment through the real inari-server membership API**
   (`PUT /api/v1/tenants/{org}/members/{subject}`) — never KC groups, never
   direct DB. A broken roles/members API fails the seed loudly (P0 signal).
5. **The default tenant, cluster, and git-config** (`e2e-org`, `e2e-self`,
   `e2e-org-inari-state` — the server's bare-name convention for the local
   provider), plus the **git write-path proof**: a `git ls-remote` poll
   against `E2E_GIT_URL` until the tenant's state repo is served
   (server → PVC → git-daemon; the tenant-created RBAC-materialization
   event commits the baseline there — no synthetic trigger needed).
6. **The `e2e-handoff` ConfigMap** — `namespace`, `toolbox`, `tenant`,
   `kc_uid`, `org_kc_id`, `cluster_id`, `org_id`, `agent_namespace` (the
   exact key set `scripts/e2e/stack/suite/env.go` reads via
   `E2E_HANDOFF_CONFIGMAP`) — including ensuring the **toolbox pod** the
   Ginkgo suite uses as its curl transport.

## Persona rules (verbatim from `scripts/e2e/ui/README.md` — hard rule 6)

- Personas are fixed; specs must NEVER mutate the role assignments of
  `e2e-operator` / `e2e-editor` / `e2e-viewer`, and never touch `dev-admin`.
- `e2e-member` is the only persona whose role may change; the role-lifecycle
  spec owns it and MUST restore it to role-less. The seed detects a leaked
  role on `e2e-member` and **warns** — it never silently repairs spec state.
- KC users are NOT added to `platform-admins` (only `dev-admin` and the
  non-persona `e2e-ops` ops identity are).

`e2e-ops` is **not a test persona**: it exists so the janitor CronWorkflow
can *approve* decommission requests it did not create (a requester may never
decide their own request). It carries no tenant role assignment, appears in
no suite, and must never be used as a test identity.

## Environment

| Variable | Required | Default | Meaning |
|---|---|---|---|
| `E2E_KC_URL` | yes | — | Keycloak base URL (one URL serves browser + API + in-cluster) |
| `E2E_BASE_DOMAIN` | in CM mode | — | public base domain; derives `https://$DOMAIN` (whitelist origin) + the API base |
| `E2E_TENANT_NAME` / `E2E_TENANT_SLUG` | no | `E2E Org` / `e2e-org` | the seeded tenant |
| `E2E_NAMESPACE` | no | CM namespace, else `inari` | namespace for the toolbox + the admin-secret fallback read |
| `E2E_GIT_URL` | no | — (proof skipped) | in-cluster git service base (`git://…:9418`) |
| `E2E_AGENT_NAMESPACE` | no | `inari-system` | agent chart namespace — published as the handoff's `agent_namespace` key (live env: `inari-e2e-agent`) |
| `E2E_USER` / `E2E_PASSWORD` | no | `dev-admin` / `dev-admin` | the dev-admin persona |
| `E2E_{OPERATOR,EDITOR,VIEWER,MEMBER}_PASSWORD` | no | = username | persona passwords (mounted from `inari-e2e-personas`) |
| `E2E_OPS_USER` / `E2E_OPS_PASSWORD` | no | `e2e-ops` / `e2e-ops` | non-persona platform-ops identity (janitor approve step; secret key `ops-password`) |
| `E2E_RBAC_VIEWER_PASSWORD` | no | `rbac-viewer` | password of the `rbac-viewer` probe identity (Ginkgo RBAC phase's `kubectl auth can-i` subject; KC user + `/tenant-<slug>/viewers` group membership only — no org join, no role assignment; mirrors the suite's `kc.PersonaPassword` resolution) |
| `SEED_KC_ADMIN_CLIENT_SECRET` | no | read from secret `inari-keycloak-admin` | the `inari-platform-admin` client secret |
| `E2E_HANDOFF_CONFIGMAP` | CM mode | — | `namespace/name` of the handoff ConfigMap; unset = local mode |
| `E2E_TOOLBOX_NAME` | no | `e2e-toolbox` | toolbox pod name |
| `E2E_KUBECONFIG` | local attach | in-cluster SA | forces the kubectl transport instead of SA REST |
| `API_BASE` | no | derived | API base override (local debugging only) |

Secrets arrive via `secretKeyRef` env mounts — never workflow parameters
(they would land in the Argo UI).

## Run modes

- **ConfigMap mode** (`E2E_HANDOFF_CONFIGMAP=ns/name` set): full phases —
  KC repair, tenant/cluster/git-config, personas, handoff publish + toolbox.
  This is the workflow path.
- **Local mode** (unset): KC repair + tenant + personas + git-config only.
  Used against the kind dev loop, where the stack provisioner owns the
  handoff file — the seed never writes one.

```sh
# local attach to the live env (kubeconfig context with CM/pod rights).
# NOTE: E2E_GIT_URL is a cluster-internal address — run the seed from a pod
# in inari-e2e (or drop the variable to skip the git write-path proof) when
# attaching from a workstation.
E2E_KUBECONFIG=~/.kube/config \
E2E_KC_URL=https://auth-e2e.7kgroup.org \
E2E_BASE_DOMAIN=inari-e2e.7kgroup.org \
E2E_HANDOFF_CONFIGMAP=inari-e2e/e2e-handoff \
E2E_GIT_URL=git://git-service.inari-e2e.svc:9418 \
E2E_PASSWORD=… E2E_OPERATOR_PASSWORD=… … \
SEED_KC_ADMIN_CLIENT_SECRET=… \
go run ./scripts/e2e/seed
```

## Workflow step contract (for the e2e-stack WorkflowTemplate)

- **Image:** `golang:1.26` + this repo checked out at the run's
  `charts-ref`; command `go run ./scripts/e2e/seed` (cold build ~1–2 min).
- **Env:** the table above; persona passwords + `SEED_KC_ADMIN_CLIENT_SECRET`
  via `secretKeyRef` (`inari-e2e-personas` keys
  `{dev-admin,operator,editor,viewer,member,ops}-password`;
  `inari-keycloak-admin` key `client-secret`).
- **SA RBAC (ns inari-e2e):** `configmaps` get/create/update (handoff —
  PUT with resourceVersion, deliberately no patch),
  `pods` get/create (toolbox), `secrets` get (admin-secret fallback only —
  not needed when the env mount is wired).
- **Ordering:** env-prep → seed → suite legs. Suite legs get
  `E2E_SEEDED=true` + `E2E_HANDOFF_CONFIGMAP=inari-e2e/e2e-handoff` injected
  by the template, gated on this step's success.
- **Budget:** < 3 min warm, hard timeout 10 min.

## Tests

```sh
cd scripts/e2e/seed && go test ./...
```

Pure decision logic (`plan.go`), client wire shapes (`clients_test.go`),
handoff contract (`handoff_test.go`). The live behavior is exercised
against the e2e environment per the W3a verification plan.
