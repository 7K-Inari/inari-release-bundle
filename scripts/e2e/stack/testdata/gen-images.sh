#!/usr/bin/env bash
# Regenerates testdata/images.txt — the CI image cache manifest.
#
# Images are extracted MECHANICALLY from the pinned charts/manifests (helm
# template at the exact versions the suite installs) so a version bump can
# never leave a stale image ref behind. Never hand-add a mutable tag
# (:latest or a moving major/minor tag) — the actions/cache key is a hash
# of this file and caching a mutable tag serves stale images silently.
#
# Pins here MUST match the suite: suite/provision.go (--version flags +
# e2e --set image pins), suite/provide.go (defaultKindNodeImage),
# scripts/install-operators.sh (CNPG_VERSION / KC_VERSION), and the chart
# values under charts/. The header of images.txt lists the exclusions.
#
# Usage: scripts/e2e/stack/testdata/gen-images.sh > /tmp/images.txt
#        (review the diff, then replace testdata/images.txt)
set -euo pipefail

# --- pins (keep in sync with the sources listed above) ---
KIND_NODE_IMAGE="kindest/node:v1.32.0@sha256:c48c62eac5da28cdadcf560d1d8616cfa6783b58f0d94cf63ad1bf49600cb027"
CNPG_CHART_VERSION="0.29.0"      # scripts/install-operators.sh CNPG_VERSION
KC_VERSION="26.3.2"              # scripts/install-operators.sh KC_VERSION
KEYCLOAK_IMAGE="quay.io/keycloak/keycloak:26.3.2"          # charts/inari-platform values
KEYCLOAK_CONFIG_CLI="adorsys/keycloak-config-cli:6.5.1-26" # charts/inari-platform values
NATS_CHART_VERSION="2.15.1"      # suite/provision.go installNATS
OPENFGA_CHART_VERSION="0.2.27"   # suite/provision.go installOpenFGA
VAULT_CHART_VERSION="0.34.1"     # suite/provision.go installVaultESO
ESO_CHART_VERSION="2.11.0"       # suite/provision.go installVaultESO
REDIS_IMAGE="bitnamilegacy/redis:8.2.1"  # suite/provision.go installServer (HA; registry pinned to docker.io so the rendered ref matches this name)
CONSOLE_NGINX="nginx:1.27-alpine"            # charts/inari-console values
CONSOLE_ORAS="ghcr.io/oras-project/oras:v1.3.0" # charts/inari-console values
CURL_IMAGE="curlimages/curl:8.10.1"      # toolbox + platform verify jobs

# CNPG operator's default postgres operand image (build constant of the
# operator the pinned chart installs) — source: pkg/versions/versions.go
# DefaultImageName at the operator tag the chart renders.
CNPG_POSTGRES_IMAGE="ghcr.io/cloudnative-pg/postgresql:18.4-system-trixie"

helm repo add cnpg https://cloudnative-pg.github.io/charts >/dev/null 2>&1 || true
helm repo add nats https://nats-io.github.io/k8s/helm/charts/ >/dev/null 2>&1 || true
helm repo add openfga https://openfga.github.io/helm-charts >/dev/null 2>&1 || true
helm repo add hashicorp https://helm.releases.hashicorp.com >/dev/null 2>&1 || true
helm repo add external-secrets https://charts.external-secrets.io >/dev/null 2>&1 || true
helm repo update >/dev/null

# chart_images renders the pinned chart and prints unique image refs.
chart_images() {
	local chart="$1" version="$2"
	helm template gen "$chart" --version "$version" 2>/dev/null |
		grep -oE 'image: *"?[^ "]+' | sed -E 's/^image: *"?//' | sort -u
}

echo "# kind node image (suite default KIND_NODE_IMAGE)"
echo "$KIND_NODE_IMAGE"
echo
echo "# operators (CNPG chart $CNPG_CHART_VERSION, Keycloak $KC_VERSION)"
chart_images cnpg/cloudnative-pg "$CNPG_CHART_VERSION"
echo "$CNPG_POSTGRES_IMAGE"
curl -sf "https://raw.githubusercontent.com/keycloak/keycloak-k8s-resources/$KC_VERSION/kubernetes/kubernetes.yml" |
	grep -oE 'image: *[^ ]+' | sed 's/^image: *//' | sort -u
echo
echo "# platform"
echo "$KEYCLOAK_IMAGE"
echo "$KEYCLOAK_CONFIG_CLI"
echo
echo "# nats/nats chart $NATS_CHART_VERSION"
chart_images nats/nats "$NATS_CHART_VERSION"
echo
echo "# openfga/openfga chart $OPENFGA_CHART_VERSION"
chart_images openfga/openfga "$OPENFGA_CHART_VERSION"
echo
echo "# hashicorp/vault chart $VAULT_CHART_VERSION (dev server only; injector disabled)"
# Only the server image is deployed in e2e (injector/CSI disabled) — render
# with the e2e sets so disabled-component images stay out of the manifest.
helm template gen hashicorp/vault --version "$VAULT_CHART_VERSION" \
	--set server.dev.enabled=true --set injector.enabled=false 2>/dev/null |
	grep -oE 'image: *"?[^ "]+' | sed -E 's/^image: *"?//' | sort -u
echo
echo "# external-secrets/external-secrets chart $ESO_CHART_VERSION"
chart_images external-secrets/external-secrets "$ESO_CHART_VERSION"
echo
echo "# bitnami/redis subchart (HA leg; e2e pins bitnamilegacy — never cache :latest)"
echo "$REDIS_IMAGE"
echo
echo "# console + tooling"
echo "$CONSOLE_NGINX"
echo "$CONSOLE_ORAS"
echo "$CURL_IMAGE"
