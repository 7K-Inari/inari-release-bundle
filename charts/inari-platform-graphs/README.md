# inari-platform-graphs

Platform resource graphs for the Inari hub cluster (operator sunset, plan
Phase 0/1, decisions D1/D3/D6/D7):

- **KRO ResourceGraphDefinition `tenantnamespace`** — generates
  `TenantNamespace.stacks.inari.io/v1alpha1` (Namespaced) and reconciles, per
  instance: a Namespace (named `spec.namespace`), a `tenant-admin`
  RoleBinding (ClusterRole `admin`; Group subject `spec.adminGroup` when
  set), a `tenant-default-deny` NetworkPolicy (Ingress+Egress, empty
  podSelector), and an optional `tenant-quota` ResourceQuota
  (`cpu` → requests+limits.cpu, `memory` → requests+limits.memory,
  `pods` → pods). Instances expose a single `Ready` condition and
  `status.namespace` (agent/inventory contract, D6).
- **Crossplane XRDs + Compositions** `identity.inari.io/v1alpha1` —
  `TenantRealm` and `TenantClient` claims backed by provider-keycloak
  (namespaced managed resources, Crossplane v2). `TenantRealm` composes a
  `Realm` and projects `status.realm`/`status.issuerURL`; `TenantClient`
  composes an openid `Client` (confidential by default; `public: true` maps
  to `accessType: PUBLIC` and skips the connection secret) and projects
  `status.clientID`/`status.realm`/`status.secretName`.

Both replicate the behavior of the retired `inari-operator` reconcilers
(`TenantNamespace`, `KeycloakRealm`, `KeycloakClient`) as declarative
gitops content. Child names are literal; impersonation is dropped (D3);
there is no adoption/migration of operator-created resources (D7).

## Prerequisites (installed by the hub gitops stack, gitops/operators)

| Feature | Requires |
|---------|----------|
| `tenantNamespace.enabled` | kro |
| `keycloak.enabled` | Crossplane (v2), provider-keycloak, `function-go-templating`; the chart can render the `ClusterProviderConfig` itself (`keycloak.providerConfig`) — only the credentials Secret (provider-keycloak JSON format) is external |
| client secret delivery | ESO (server renders the per-tenant `SecretStore`/`ExternalSecret`, see `examples/`) |

## Values

| Key | Default | Description |
|-----|---------|-------------|
| `tenantNamespace.enabled` | `true` | Render the `tenantnamespace` RGD |
| `keycloak.enabled` | `false` | Render the Keycloak XRDs + Compositions (requires `issuerBaseURL`; the gitops Application opts in) |
| `keycloak.providerConfigRef` | `keycloak` | provider-keycloak `ClusterProviderConfig` name |
| `keycloak.issuerBaseURL` | `""` (required) | Public Keycloak base URL; `status.issuerURL = <issuerBaseURL>/realms/<realm>` |
| `keycloak.providerConfig.enabled` | `true` | Render the `ClusterProviderConfig` the compositions reference |
| `keycloak.providerConfig.credentialsSecretRef` | `inari-keycloak-admin`/`inari`/`credentials` | Secret holding provider-keycloak's credentials JSON (`{client_id, client_secret, url}`); the `credentials` key on the inari-platform admin secret is Phase 3 wiring |

## Client credential delivery

`TenantClient` (confidential) → provider-keycloak writes
`<claim-name>-keycloak-connection` (key `attribute.client_secret`) into the
claim's namespace → the server-rendered `ExternalSecret` materializes
`<secretName>` (default `<claim>-keycloak-client`) with keys
`client-id` / `client-secret` / `issuer`, mirroring the operator's Secret
contract. **Public clients** (`public: true`) get no connection secret; the
server renders a plain Secret holding only `client-id`/`issuer` in that
case (the operator also wrote the Secret for public clients, minus the
secret key). See `examples/keycloak-client-secret-delivery.yaml` for the
exact per-tenant `ServiceAccount`/`Role`/`RoleBinding`/`SecretStore`/
`ExternalSecret` bundle the server renders.

## Examples

`examples/` (not rendered by helm): a TenantNamespace instance, TenantRealm /
TenantClient claims, and the ESO secret-delivery bundle. These are the
manifest shapes the server commits to `tenants/<org>/` in the hub-facing
platform repo.

## Validation

- `helm unittest charts/inari-platform-graphs` — golden suites for the RGD
  and the claims (also run in CI).
- `make validate-graphs` — renders the chart and validates the RGD /
  XRD / Composition manifests against the pinned KRO, Crossplane and
  provider-keycloak CRD schemas (kubeconform).
- Kind check (requires docker + kind + kubectl):

```sh
kind create cluster --name graphs-check
helm install kro oci://registry.k8s.io/kro/charts/kro --version 0.9.4 \
  -n kro --create-namespace   # or the W1-1 pin in gitops/operators/kro.yaml
helm install graphs charts/inari-platform-graphs \
  --set keycloak.enabled=false   # keycloak claims need Crossplane+provider
kubectl wait --for=condition=Ready rgd/tenantnamespace --timeout=120s
kubectl create namespace inari   # instance namespace (server's choice in prod)
kubectl apply -f charts/inari-platform-graphs/examples/tenantnamespace.yaml \
  -n inari
kubectl get tenantnamespace tenant-acme -n inari \
  -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}'   # True
kubectl get namespace tenant-acme --show-labels                    # tenant label
kubectl get rolebinding tenant-admin -n tenant-acme -o yaml
kubectl get networkpolicy tenant-default-deny -n tenant-acme -o yaml
kubectl delete tenantnamespace tenant-acme -n inari               # children gone
kind delete cluster --name graphs-check
```
