#!/usr/bin/env bash
# validate-graphs.sh — offline validation of charts/inari-platform-graphs.
#
# 1. helm template -> kubeconform against JSON schemas derived from the
#    PINNED upstream CRDs (kro ResourceGraphDefinition, Crossplane
#    XRD/Composition, provider-keycloak Realm/Client).
# 2. Reference check: every apiVersion composed by files/*.gotmpl must exist
#    among those pinned CRDs (i.e. the compositions are validated against the
#    provider-keycloak CRD package at PROVIDER_KEYCLOAK_VERSION).
#
# Version pins MUST stay aligned with the hub installs in gitops/operators
# (task W1-1). Override via env when they drift.
set -euo pipefail

CHART_DIR="${CHART_DIR:-charts/inari-platform-graphs}"
KRO_VERSION="${KRO_VERSION:-v0.9.4}"
CROSSPLANE_VERSION="${CROSSPLANE_VERSION:-v2.4.3}"
PROVIDER_KEYCLOAK_VERSION="${PROVIDER_KEYCLOAK_VERSION:-v3.2.3}"
KUBECONFORM_VERSION="${KUBECONFORM_VERSION:-v0.7.0}"

CRD_URLS=(
  "https://raw.githubusercontent.com/kubernetes-sigs/kro/${KRO_VERSION}/helm/crds/kro.run_resourcegraphdefinitions.yaml"
  "https://raw.githubusercontent.com/crossplane/crossplane/${CROSSPLANE_VERSION}/cluster/crds/apiextensions.crossplane.io_compositeresourcedefinitions.yaml"
  "https://raw.githubusercontent.com/crossplane/crossplane/${CROSSPLANE_VERSION}/cluster/crds/apiextensions.crossplane.io_compositions.yaml"
  "https://raw.githubusercontent.com/crossplane-contrib/provider-keycloak/${PROVIDER_KEYCLOAK_VERSION}/package/crds/realm.keycloak.m.crossplane.io_realms.yaml"
  "https://raw.githubusercontent.com/crossplane-contrib/provider-keycloak/${PROVIDER_KEYCLOAK_VERSION}/package/crds/openidclient.keycloak.m.crossplane.io_clients.yaml"
  "https://raw.githubusercontent.com/crossplane-contrib/provider-keycloak/${PROVIDER_KEYCLOAK_VERSION}/package/crds/keycloak.m.crossplane.io_clusterproviderconfigs.yaml"
)

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
CRD_DIR="$WORK/crds"
SCHEMA_DIR="$WORK/schemas"
mkdir -p "$CRD_DIR" "$SCHEMA_DIR"

echo ">> fetching pinned CRDs (kro ${KRO_VERSION}, crossplane ${CROSSPLANE_VERSION}, provider-keycloak ${PROVIDER_KEYCLOAK_VERSION})"
for url in "${CRD_URLS[@]}"; do
  f="$CRD_DIR/$(basename "$url")"
  curl -fsSL "$url" -o "$f"
done

# Convert each CRD version's openAPIV3Schema into a kubeconform-consumable
# JSON schema at <group>/<kind>_<version>.json (matches -schema-location;
# kubeconform's .ResourceKind is lowercase).
convert_crd() {
  local file="$1" group kind kindlc count i served vname
  group="$(yq -r '.spec.group' "$file")"
  kind="$(yq -r '.spec.names.kind' "$file")"
  kindlc="$(echo "$kind" | tr '[:upper:]' '[:lower:]')"
  mkdir -p "$SCHEMA_DIR/$group"
  count="$(yq '.spec.versions | length' "$file")"
  for ((i = 0; i < count; i++)); do
    served="$(yq -r ".spec.versions[$i].served // true" "$file")"
    [ "$served" = "true" ] || continue
    vname="$(yq -r ".spec.versions[$i].name" "$file")"
    yq -o=json "
      .spec.versions[$i].schema.openAPIV3Schema as \$s |
      {
        \"\$schema\": \"http://json-schema.org/schema#\",
        \"type\": \"object\",
        \"required\": [\"apiVersion\", \"kind\"],
        \"properties\": ({
          \"apiVersion\": {\"enum\": [\"${group}/${vname}\"]},
          \"kind\": {\"enum\": [\"${kind}\"]},
          \"metadata\": {\"type\": \"object\"},
          \"spec\": \$s.properties.spec,
          \"status\": \$s.properties.status
        } | del(.status | select(. == null)))
      }" "$file" > "$SCHEMA_DIR/${group}/${kindlc}_${vname}.json"
  done
}
for f in "$CRD_DIR"/*.yaml; do convert_crd "$f"; done
echo ">> derived schemas:"
find "$SCHEMA_DIR" -type f | sed "s|$SCHEMA_DIR/|   |"

echo ">> helm template"
helm template graphs "$CHART_DIR" \
  --set keycloak.enabled=true \
  --set keycloak.issuerBaseURL=https://keycloak.example.org \
  > "$WORK/rendered.yaml"

if ! command -v kubeconform >/dev/null 2>&1; then
  echo ">> kubeconform not found; downloading ${KUBECONFORM_VERSION}"
  curl -fsSL "https://github.com/yannh/kubeconform/releases/download/${KUBECONFORM_VERSION}/kubeconform-linux-amd64.tar.gz" \
    | tar -xz -C "$WORK" kubeconform
  KUBECONFORM="$WORK/kubeconform"
else
  KUBECONFORM="kubeconform"
fi

echo ">> kubeconform against pinned CRD schemas"
"$KUBECONFORM" -strict -summary \
  -schema-location default \
  -schema-location "${SCHEMA_DIR}/{{.Group}}/{{.ResourceKind}}_{{.ResourceAPIVersion}}.json" \
  "$WORK/rendered.yaml"

echo ">> checking composed managed-resource apiVersions against provider-keycloak CRDs"
missing=0
while IFS= read -r gv; do
  group="${gv%%/*}"
  # Only Crossplane managed resources are checked; the identity.inari.io
  # write-back document targets our own XRD (validated above).
  case "$group" in
    *.crossplane.io) ;;
    *) echo "   skip (own claim API): ${gv}"; continue ;;
  esac
  if [ -d "$SCHEMA_DIR/$group" ]; then
    echo "   ok: ${gv}"
  else
    echo "   MISSING CRD schema for composed apiVersion ${gv}"
    missing=1
  fi
done < <(grep -hoE '^apiVersion: [a-z0-9./-]+' "$CHART_DIR"/files/*.gotmpl | awk '{print $2}' | sort -u)
[ "$missing" -eq 0 ] || { echo "composed resource reference check FAILED"; exit 1; }

echo "validate-graphs: OK"
