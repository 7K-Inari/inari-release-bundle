# scripts/e2e/ui — console UI e2e (Playwright Test)

Browser-level e2e for the Inari console. Runs against the persistent e2e
environment (`https://inari-e2e.7kgroup.org` — console + API single origin,
API at `/api/v1`) as a leg of the Argo `e2e-stack` WorkflowTemplate, after
the seed step (`scripts/e2e/seed`). Replaces the former
`scripts/e2e/ui-smoke.mjs` raw-Playwright script.

## Layout

```
scripts/e2e/ui/
├── playwright.config.ts        # local defaults (list reporter, no retries)
├── playwright.config.ci.ts     # CI: retries 2, workers 1, screenshot/video/trace on failure,
│                               #   JUnit + HTML + JSON (+ ReportPortal when RP_* envs are set)
├── quarantine-issues.mjs       # NIGHTLY ONLY: ReportPortal-fed quarantine pipeline — files/dedupes
│                             #   `e2e-flake` issues for tests unstable in 2+ nightly launches within
│                             #   7 days, enforces the 14/30-day expiry policy, maintains the pinned
│                             #   [quarantine-budget] issue (docs/e2e/test-taxonomy.md §4)
├── quarantine-issues.test.mjs  # unit tests (node --test) for the classification/dedupe/expiry logic
├── fixtures/
│   ├── auth.ts                 # persona registry (5 personas) + KC two-step login helper
│   ├── auth.setup.ts           # setup project: saves .auth/<persona>.json for every persona
│   ├── org.ts                  # unique-name factories: e2e-<runId>-<seq>[-<kind>]
│   └── key-pages.ts            # shared live-page registry/navigation for visual + a11y
├── helpers/
│   ├── env.ts                  # THE env contract (single process.env reader — see below)
│   ├── env.test.ts             # contract tests: live/legacy resolution, E2E_SEEDED fail-fast
│   ├── poll.ts                 # poll(fn, { timeout, interval }) — see the polling RULE below
│   ├── poll.test.ts            # poll contract tests (node --test)
│   ├── git.ts                  # git ls-remote probe for the git-daemon precondition
│   ├── git.test.ts             # unit tests (local bare repos; no daemon needed)
│   ├── api.ts                  # thin inari-server REST client (direct-grant token; reads +
│   │                           #   DELETE for spec-owned cleanup only + raw() for
│   │                           #   negative-path assertions — see rule 5 below)
│   ├── a11y.ts                 # standard axe scan + accepted/stale violation partitioning
│   └── a11y.test.ts            # unit tests for the pure violation helpers
├── pages/
│   ├── login.page.ts           # KC 26 two-step form (username → submit → password)
│   ├── app-shell.page.ts       # boot gate, tenant cards, sidebar nav, tenant switcher,
│   │                           #   "Loading…" waits
│   ├── catalog.page.ts         # Catalog browse marker
│   ├── deploys.page.ts         # Deploys (resource inventory) marker
│   ├── access/                 # Access section + Members / Teams & Roles / Roles / Identity tabs
│   ├── organizations/          # create-organization form (ids from inari-ui)
│   └── settings/               # Settings inner nav (admin-filtered), read-only notice,
│                               #   Org profile / Git config sub-pages
└── specs/
    ├── visual/
    │   └── key-pages.spec.ts   # @p2 @visual — four live-page baselines (+ adjacent snapshots)
    ├── a11y/
    │   └── key-pages.spec.ts   # @p2 @a11y — axe on the shell/dashboard/Access tabs/profile
    ├── smoke/
    │   └── access-tabs.spec.ts # @p0 @smoke — the four RBAC tabs render their markers
    ├── settings/
    │   ├── org-profile.spec.ts # @p1 @settings — display-name write path: save → reload →
    │   │                       #   persisted → audit event via the API; restores the shared
    │   │                       #   org's original name at the end (spec independence)
    │   └── git-config.spec.ts  # @p1 @settings — state-repo write path; git-daemon
    │                           #   precondition via E2E_GIT_URL (in-cluster git service);
    │                           #   restores the original config at the end
    └── rbac/
        ├── role-lifecycle.spec.ts # @p0 @rbac — custom role CRUD, team→role mapping,
        │                          #   propagation to e2e-member's /me/permissions,
        │                          #   delete-in-use 409, cleanup
        ├── builtin-protection.spec.ts # @p0 @rbac — built-in role rename/delete 409s (UI + API)
        │                              #   and the tenant.admin last-admin guardrail (P0-3/P0-4)
        └── role-gating-matrix.spec.ts # @p1 @rbac — per-persona gating matrix (P1-1): each
                                       #   role persona's control set vs its migration-0029
                                       #   bundle, negative direct-URL nav, bundle regression
                                       #   guard (API)
    └── tenants/
        ├── onboarding.spec.ts    # @p1 @tenants — org creation through the console UI,
        │                         #   tenant landing, Access tabs in the fresh org,
        │                         #   API metadata assertion, duplicate-slug 409 (P1-4)
        └── switching.spec.ts     # @p1 @tenants — tenant switching via the header
                                  #   switcher: e2e-org ↔ API-created org, per-context
                                  #   data scoping (Members, Settings slug), switcher
                                  #   indicator, reload persistence (P1-5)
```

Both tenant specs **intentionally do not delete** the orgs they create
(DELETE `/api/v1/tenants/{org}` is approval-gated and asynchronous — there
is no clean synchronous delete). Each spec creates its own org
(`uniqueName("onb")` / `uniqueName("sw")`); the switching spec is
self-contained and never depends on the onboarding spec's leftovers. On the
persistent env the janitor CronWorkflow reaps the `e2e-<runId>-*`
leftovers — this is why `E2E_RUN_ID` is part of the env contract.

The role-lifecycle spec's propagation step polls `GET /me/permissions` with
`e2e-member`'s token for up to **120s** (5s interval) — that budget covers
the OrgTeamSync interval plus the `/me/permissions` cache TTL and dominates
the spec's ~90s expected runtime; the describe sets a 180s test timeout so
the project default (60s) can't kill the poll.

## Running against the live e2e environment

The suite's target is the persistent e2e environment; a seeded env is the
only prerequisite (the seed step — `scripts/e2e/seed` — must have run since
the last env-prep; on the workflow this is guaranteed by step ordering).

```sh
cd scripts/e2e/ui
npm ci --include=dev
npx playwright install --with-deps chromium

export E2E_BASE_DOMAIN=inari-e2e.7kgroup.org
export E2E_KC_URL=https://auth-e2e.7kgroup.org
export E2E_SEEDED=true                    # fail-fast guard (contract invariant 4)
export E2E_RUN_ID=local-$(date +%s)       # feeds uniqueName(); the janitor's reap prefix
export E2E_GIT_URL=git://git-service.inari-e2e.svc:9418   # only reachable in-cluster
# Persona passwords come from the Vault-backed inari-e2e-personas secret:
export E2E_PASSWORD=… E2E_OPERATOR_PASSWORD=… E2E_EDITOR_PASSWORD=… \
       E2E_VIEWER_PASSWORD=… E2E_MEMBER_PASSWORD=…

npx playwright test                                                 # local config
npx playwright test --config playwright.config.ci.ts --grep "@p0|@smoke" --grep-invert @quarantine
```

`E2E_GIT_URL` is a cluster-internal address (`git://…svc:9418`) — from a
workstation either skip the git-config spec (`--grep-invert @settings`,
which also drops org-profile) or run the suite from a pod in `inari-e2e`.

### Env contract (helpers/env.ts — the ONLY process.env reader in the suite)

Two modes, selected by `E2E_BASE_DOMAIN`:

| Export | Live mode (`E2E_BASE_DOMAIN` set) | Legacy mode (kind local loop) |
|---|---|---|
| `UI_BASE` | `https://$E2E_BASE_DOMAIN` | `$UI_BASE` (default `http://127.0.0.1:8080`) |
| `KC_URL` / `KC_HOST` | `$E2E_KC_URL` (required) / derived host | `$KC_URL` / `$KC_HOST` (`127.0.0.1:18091`) |
| `API_BASE` | `$API_BASE` override, else `https://$E2E_BASE_DOMAIN/api/v1` | `$API_BASE` (default `$UI_BASE`) |
| `TENANT_NAME` / `TENANT_SLUG` | `$E2E_TENANT_NAME` (`E2E Org`) / `$E2E_TENANT_SLUG` (`e2e-org`) | `$TENANT_NAME` / `$TENANT` |
| `GIT_URL` | `$E2E_GIT_URL` ("" skips the git-daemon precondition) | "" |
| `RUN_ID` | `$E2E_RUN_ID` → `$GITHUB_RUN_ID` → random hex | same |
| persona passwords | `$E2E_PASSWORD`, `$E2E_{OPERATOR,EDITOR,VIEWER,MEMBER}_PASSWORD` | defaults = username |

Live mode **fails fast at import when `E2E_SEEDED` is not `"true"`** — a run
against an unseeded env is a setup bug, not a test failure
(environment-contract invariant 4). Legacy mode skips the check (the kind
bring-up and the seed are one local flow).

### Kind local loop (legacy mode)

Requires a running golden-path stack (`cd scripts/e2e/stack && go test
-tags=e2e -count=1 -timeout 40m ./...`) plus port-forwards for the console
(18080), server (18090), and Keycloak (18091); the Go seed's local mode
(`go run ../seed` with `E2E_KC_URL=http://127.0.0.1:18091` and the legacy
`API_URL`/`UI_ORIGIN` defaults) replaces the retired `seed-personas.mjs`.
Without `E2E_BASE_DOMAIN` the suite resolves exactly the pre-migration
defaults — single origin in kind required the (deleted) ui-proxy, so point
`UI_BASE` at whatever single-origin shim your loop uses.

## Visual regression and accessibility (nightly-only)

`specs/visual/` and `specs/a11y/` use the shared live-page registry in
`fixtures/key-pages.ts`. Both layers carry **`@p2`**, so the existing release,
edge, upgrade, and smoke filters exclude them while the nightly's
`@p0|@p1|@p2` selection includes them in the existing two Playwright shards.
There is no separate workflow leg or release gate.

### Visual baselines

The initial four baselines cover the all-tenants app shell, tenant dashboard,
Access → Identity (including the four-tab strip), and Settings → Organization
profile. The home baseline clips to stable shell chrome above the
variable-height live aggregates. All baselines run against the real seeded
environment: **never stub API routes** to make a screenshot deterministic.
Mask only live data (identity, counts, tenant/member rows, timestamps, and
fields another spec may mutate); do not mask away the static layout the
baseline is meant to protect.

Screenshot defaults (`animations: "disabled"`, `caret: "hide"`, CSS scale, and
a 0.1% pixel-difference budget) live in `playwright.config.ts`. Baselines are
Linux/Chromium-specific and must be generated in the **same Playwright image
as the workflow**, not on a host browser. With the live-env variables from
"Running against the live e2e environment" exported, run from `scripts/e2e/ui`:

```sh
docker run --rm --ipc=host \
  -v "$PWD:/work" -w /work \
  -e E2E_BASE_DOMAIN -e E2E_KC_URL -e E2E_SEEDED -e E2E_RUN_ID \
  -e E2E_USER -e E2E_PASSWORD \
  -e E2E_OPERATOR_PASSWORD -e E2E_EDITOR_PASSWORD \
  -e E2E_VIEWER_PASSWORD -e E2E_MEMBER_PASSWORD \
  mcr.microsoft.com/playwright:v1.64.1-noble \
  sh -c 'npm ci --include=dev && npx playwright test --grep @visual --update-snapshots --workers=1'
```

Bump that image tag in lockstep with `@playwright/test` and the workflow.
Review every added/changed PNG in the PR; never use `--update-snapshots`
merely to make an unexplained red test green. A legitimate UI change needs a
deliberate baseline refresh; an unintended change needs an inari-ui fix.
Snapshots use Playwright's default adjacent
`key-pages.spec.ts-snapshots/*-chromium-linux.png` convention and are
committed.

### Accessibility scans

`helpers/a11y.ts` runs axe with `wcag2a`, `wcag2aa`, `wcag21a`, and `wcag21aa`.
The shell, dashboard, all four Access tabs, and org profile must have zero
unexpected violations. Fix-forward in inari-ui is the default. If a violation
is genuinely accepted, add it only to that page's `ACCEPTED` list with a
non-empty `rule`, `issue` (for example `7K-Inari/inari-ui#123`), and `reason`.
An exclusion without a tracking issue is a config error; an exclusion whose
rule stops violating fails as stale and must be removed. Selector/subtree
exclusions and blanket `disableRules` are deliberately unsupported. Accepted
violations are attached to the Playwright result for triage. The first live
baseline needed **no accepted exclusions**.

## Personas

Seeded by the Go seed step (`scripts/e2e/seed`) — run as a workflow step
between env-prep and the suite legs; idempotent, safe to re-run. One per
ADR-0013 built-in role, plus the role-less member:

| User | Password | Role | Purpose | Mutation policy |
|---|---|---|---|---|
| `dev-admin` | `$E2E_PASSWORD` (Vault `inari-e2e/personas` on the live env; `dev-admin` locally) | `admin` (platform-admins, E2E Org creator) | default `chromium` project identity | never touched by specs |
| `e2e-operator` | `$E2E_OPERATOR_PASSWORD` | `operator` (Platform Engineer) | operator-scope RBAC coverage | **specs must never change the assignment** |
| `e2e-editor` | `$E2E_EDITOR_PASSWORD` | `editor` (Developer) | developer-scope RBAC coverage | **specs must never change the assignment** |
| `e2e-viewer` | `$E2E_VIEWER_PASSWORD` | `viewer` | read-only RBAC surface | **specs must never change the assignment** |
| `e2e-member` | `$E2E_MEMBER_PASSWORD` | *(none)* | org member with no role/team — propagation target for the role-lifecycle spec | owned by the role-lifecycle spec, which **must restore it to role-less** (afterEach/afterAll) |

Role assignment goes through the real inari-server membership API
(`PUT /api/v1/tenants/{org}/members/{subject}` with a built-in `roleId`) —
never KC groups, never direct DB — so seeding doubles as a membership-path
signal. The seed fails fast if that API misbehaves (P0-class, not a seed
bug).

## Rules for spec authors

1. **Poll after every mutation.** No spec ever asserts immediately after a
   mutation. All propagation — the OrgTeamSync interval, the
   `/me/permissions` cache, the PEP TTL — is observed via
   `helpers/poll.ts` with an explicit timeout/interval.
2. **No assertions in page objects.** `pages/` exposes stable
   `getByRole`/`getByLabel` locators and intent-level actions; specs own
   every `expect()`.
3. **Tags.** Priority tags `@p0`/`@p1`/`@p2`, suite tags like `@smoke`, and
   `@quarantine` for known-flaky specs. The release gate greps `@p0` and
   inverts `@quarantine` (a quarantined test NEVER blocks a release); the
   nightly (Argo CronWorkflow `e2e-nightly`) runs the full `@p0|@p1|@p2` set
   including `@quarantine`. Flake → issue → quarantine lifecycle: see
   "Flakiness containment" in `scripts/e2e/README.md`.
4. **Personas are fixed, spec objects are unique.** Never mutate the role
   personas' assignments or `dev-admin` (see the table above); only
   `e2e-member`'s role may change, and only inside the role-lifecycle spec.
   Spec-created objects (orgs/teams/roles) go through the `fixtures/org.ts`
   unique-name factory — never create fixed-name objects that could collide
   with the seed.
5. **API client is read-only — except `raw()`.** `ApiClient.get/post` are for
   setup/assertion reads. The sole exception is `raw()`, which returns the
   status without throwing and exists only for negative-path assertions where
   the spec's subject IS the rejection (e.g. the 409 guardrails in
   `specs/rbac/`). Never use `raw()` to set up state or to bypass the UI for
   behavior a spec is meant to exercise through the console.
6. **Screenshots belong only in `specs/visual/`.** Use the shared key-page
   registry, the container-locked baseline flow, and narrow live-data masks.
   Never introduce ad-hoc screenshots in functional specs or route stubs for
   visual determinism.
7. **Axe scans are read-only and live.** Use `scanA11y()` against the real
   page; specs own the unexpected/stale-exclusion assertions. Never silence a
   rule globally or without a linked issue.

## Key constraints (learned the hard way — do not "fix")

- **Single-origin topology is required.** The server sends no CORS headers;
  the gateway routes `/api/v1` and `/` on one host. On the live env the real
  ingress provides this — there is no ui-proxy anymore (deleted with the
  kind era). In kind you must reproduce it yourself if you run the suite
  there.
- **One Keycloak URL.** On real ingress the browser-reachable URL IS the
  API-reachable URL: Keycloak hostname v2 pins the issuer to the configured
  hostname, so tokens minted through any endpoint carry the public `iss`
  (verified on the e2e env). The kind-era `/etc/hosts` issuer aliasing is
  gone; do not reintroduce a browser/API split.
- **inari-ui client redirect whitelist.** The realm re-import can wipe the
  `inari-ui` client's redirect URIs / web origins, so the seed
  (`scripts/e2e/seed`) re-merges `https://$E2E_BASE_DOMAIN/*` after every
  env-prep. The seed owns this — do not re-add it to the workflow or the
  suite.
- **The realm import suppresses built-in KC scopes.** Its explicit
  `clientScopes` array makes Keycloak skip `basic`/`organization`/etc. —
  the seed recreates and attaches them (GAP markers in
  `scripts/e2e/seed/scopes.go`). If every login dies with `invalid_scope`,
  the seed hasn't run (or the repair regressed).
