# gitops/ — ArgoCD composition of the Inari platform

The platform is composed from independently released component charts; this
directory is the single source of truth for "what runs on a platform
cluster". `make dev-up` (scripts/bootstrap.sh) installs ArgoCD on a kind
cluster and applies these manifests.

## Sync waves

| Wave | Application | Source |
|-----:|-------------|--------|
| -3 | `cert-manager` | helm `cert-manager` (jetstack), `crds.enabled=true` |
| -3 | `external-dns` | helm `external-dns` + upstream CRD manifests (multi-source; dev-safe no-op rfc2136 provider) |
| -3 | `external-secrets` | helm `external-secrets` (`installCRDs=true`) |
| -3 | `crossplane` | helm `crossplane` (stable repo) |
| -2 | `cnpg` | helm `cloudnative-pg` 0.29.0 (CloudNativePG operator) |
| -2 | `keycloak-operator` | kustomize `gitops/operators/keycloak-operator` (upstream 26.3.2 + cluster-wide patch) |
| -2 | `kro` | OCI helm `ghcr.io/kro-run/kro/kro` |
| -2 | `crossplane-provider-keycloak` | kustomize `gitops/operators/crossplane-provider-keycloak` (Provider CR; no ProviderConfig yet) |
| -1 | `inari-operator-crds` | OCI `ghcr.io/7k-inari/charts/inari-operator-crds` |
| 0 | `platform-config` | `charts/inari-platform` in this repo (chart name `inari-platform`: CNPG Cluster, `inari-db` secrets, Keycloak CR, realm import + PostSync jobs) |
| 1 | `nats`, `openfga` | upstream helm charts |
| 1 | `platform-rgd` | kustomize `gitops/platform/rgd` — tenantnamespace RGD + Keycloak compositions (content: task W1-2) |
| 2 | `inari-operator` | OCI `ghcr.io/7k-inari/charts/inari-operator` |
| 3 | `inari-server`, `inari-console` | OCI `ghcr.io/7k-inari/charts/...` (charts live in and release from this repo) |

## Hub prerequisites (waves -3/-2)

cert-manager, ExternalDNS, and ESO are prerequisites the operator's CRDs
presuppose; KRO and Crossplane (+ provider-keycloak) are the sunset target
architecture (assessment decisions D1/D6) — KRO hosts the `tenantnamespace`
ResourceGraphDefinition, Crossplane hosts the Keycloak realm/client managed
resources that replace the operator's facade CRDs.

- Hub cert-manager/ExternalDNS are **sunset-scoped**: they exist only for the
  operator's `CertIssuer`/`DNSZone`/`DNSRecord` facades and are removed in
  Phase 4. Tenant-cluster installs are first-class addons (multi-repo GitOps
  feature), never hub gitops. The hub ExternalDNS ships dev-safe values
  (CRD source only, no-op rfc2136 provider); real installs override the
  provider via values.
- `crossplane-provider-keycloak` ships the Provider CR only — ProviderConfig
  (Keycloak credentials) arrives with the RGD/compositions work (W1-2) /
  Phase 3. The Provider is Healthy without it.
- `platform-rgd` is the delivery scaffold for the tenantnamespace RGD and
  Keycloak compositions; the kustomize dir builds zero objects until W1-2
  lands content (an empty Application syncs Healthy).

Inside `platform-config`, per-resource sync waves order secrets (-1) → CNPG
Cluster (0) → Keycloak CR (1) → KeycloakRealmImport (2); the client-setup /
realm-sync / realm-verify jobs are ArgoCD `PostSync` hooks (weights 6/7/8)
whose failure fails the Application sync.

## Dex / ArgoCD SSO (Wave 3)

- Dex deploys ONE cluster-local instance per tenant cluster from the
  **official dexidp chart** (`https://charts.dexidp.io`, chart `dex`): OIDC
  pass-through to Keycloak realm `inari` as confidential client
  `cluster-<id>-dex`. The inari-operator renders a single-source ArgoCD
  `Application` per tenant cluster (pinned `targetRevision`) — no Dex
  Application lives in gitops/ and no dex chart is published from this repo.
  Production Dex config (connectors, static clients) comes only from an
  ESO/Vault-synced Secret (`configSecret.create: false` +
  `configSecret.name`); the operator also renders the tenant ArgoCD OIDC/RBAC
  baseline (`argocd-cm`, `argocd-rbac-cm`) alongside it.
- The `inari-platform` chart renders those `cluster-<id>-dex` clients into
  the realm import from `keycloak.dexClusters[]` (realm-creation placeholder
  secrets; runtime provisioning/rotation is owned by inari-server, W2).
- ArgoCD per-user SSO maps Keycloak groups (via the Dex `groups` claim) to
  ArgoCD roles through the operator-rendered `argocd-rbac-cm` (fail-closed
  `policy.default`). Static `accounts.inari-breakglass` stays as the
  documented break-glass fallback (password set out-of-band via the argocd
  CLI, never stored by the operator).
- Dev/kind: `gitops/dev/dex.yaml` is a standalone Application for the
  upstream chart with an inline mock-connector config (no secrets needed).
  Apply it manually (`kubectl apply -f gitops/dev/`) when a local tenant
  cluster needs a Dex; it is never applied by bootstrap.sh.

## Notes

- `inari-server` / `inari-console` charts live in this repo
  (`charts/inari-server`, `charts/inari-console`) and release to the
  org-level OCI namespace `oci://ghcr.io/7k-inari/charts`. Their
  Applications pin the first centrally-released versions and stay OutOfSync
  until those publish; bootstrap treats them as optional meanwhile.
- `dataProtection.keepOnUninstall=true` in platform-config marks the CNPG
  Cluster `Prune=false` — enable it for any environment with data.
- Dev passwords live inline in these dev Applications (kind only); real
  installs use `postgresql.auth.existingSecret` (e.g. Vault/ESO).
