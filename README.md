# inari-release-bundle

Release bundle for the Inari platform: the core Helm charts
(`charts/inari-platform`, `charts/inari-server`, `charts/inari-console`),
GitOps composition and end-to-end testing for the platform
cluster (ArgoCD Application definitions in `gitops/`), day-0 bootstrap and
backup/restore/DR tooling (plan §6 #10, §9 M0).

Stack: ArgoCD (App-of-Apps-style sync waves) + Helm charts released as OCI.

All charts publish to the org-level OCI namespace
`oci://ghcr.io/7k-inari/charts` (shared with the inari-agent chart, which
stays in its own repo). Charts version
independently via release-please; `charts/inari-server` and
`charts/inari-console` get their `appVersion` bumped automatically by the
component release pipelines (see `.github/workflows/chart-sync.yml`) so the
charts release in lockstep with inari-server/inari-ui. The
`charts/inari-platform` chart also declares the supported inari-agent
version range (`agent.supportedRange`/`agent.recommended`, rendered into the
`inari-agent-compat` ConfigMap).

Every merge to `main` also cuts edge releases: per-chart OCI tags and GitHub
prereleases at `<pending-version>-<shortsha>`, plus the moving `edge`
channel tag/release (see `.github/workflows/edge.yml`). Stable releases are
human-gated via the release-please Release PR.

Part of the **Inari** multi-tenant Internal Developer Platform (GitHub org `7K-Inari`).
Canonical architecture & development plan: [inari-docs/docs/architecture/inari-platform-plan.md](https://github.com/7K-Inari/inari-docs/blob/main/docs/architecture/inari-platform-plan.md)

## Layout

- `gitops/` — ArgoCD Applications composing the platform from component
  charts (see [gitops/README.md](gitops/README.md) for the sync-wave table).
- `charts/inari-platform` — platform glue: CNPG PostgreSQL Cluster + db
  secrets, Keycloak instance and realm `inari` glue (realm import/sync/verify
  as ArgoCD PostSync hooks), inari-agent compatibility declaration.
- `charts/inari-server`, `charts/inari-console` — control-plane API server
  and web console charts (moved from the component repos).
- `scripts/` — day-0 bootstrap, backup/restore, DR drill.
- The `inari-agent` chart stays in its component
  repo and publishes to the same org-level OCI namespace.

## Quickstart (day-0 bootstrap)

Prereqs: `docker`, `kind`, `kubectl`, `jq`.

```sh
make dev-up     # kind cluster + ArgoCD + gitops/ apps + readiness waits + smoke check
make dev-down   # tear down
```

The composed stack provides Keycloak (realm `inari`), PostgreSQL
(CloudNativePG), NATS (JetStream) and OpenFGA, plus `inari-server` and
`inari-console` Applications once their charts are published.

## Backup / restore / DR drill

```sh
make backup                          # tarball into ./backups/
make restore BACKUP=backups/…tgz     # restore onto an existing stack
make dr-drill BACKUP=backups/…tgz    # fresh kind cluster -> restore -> verify (M0 gate)
```

See [docs/backup-restore.md](docs/backup-restore.md).

## Development

```sh
make lint    # helm lint + chart-testing + gitops manifest validation
make test    # helm-unittest
```

All charts are published as OCI artifacts to `oci://ghcr.io/7k-inari/charts`
by `release.yaml` (release-please tags `<chart>-vX.Y.Z`). The old
repo-scoped namespace `oci://ghcr.io/7k-inari/inari-helm-charts/charts` is
deprecated; existing pins keep working but new releases only go to the
org-level namespace.
