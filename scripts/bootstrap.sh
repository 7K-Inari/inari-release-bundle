#!/usr/bin/env bash
# Day-0 bootstrap: create a kind cluster, install ArgoCD and apply the
# gitops/ Application definitions (Inari must never require Inari).
#
# Idempotent: reuses an existing kind cluster; kubectl apply is a no-op
# when nothing changed.
#
# Usage: scripts/bootstrap.sh [--skip-kind]

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib/common.sh
source "$SCRIPT_DIR/lib/common.sh"

SKIP_KIND=false
[[ "${1:-}" == "--skip-kind" ]] && SKIP_KIND=true

require kubectl chainsaw
$SKIP_KIND || require kind

if ! $SKIP_KIND; then
  if kind get clusters 2>/dev/null | grep -qx "$KIND_CLUSTER_NAME"; then
    log "kind cluster '$KIND_CLUSTER_NAME' already exists, reusing"
  else
    log "creating kind cluster '$KIND_CLUSTER_NAME'"
    kind create cluster --name "$KIND_CLUSTER_NAME"
  fi
  kubectl config use-context "kind-${KIND_CLUSTER_NAME}" >/dev/null
fi

"$SCRIPT_DIR/install-argocd.sh"

log "applying gitops/ Application definitions"
kubectl apply -f "$GITOPS_DIR/argocd-project.yaml"
kubectl apply -f "$GITOPS_DIR/operators"
kubectl apply -f "$GITOPS_DIR/platform"
kubectl apply -f "$GITOPS_DIR/apps"

# CI/e2e: pin git-sourced Applications at the branch under test. HEAD would
# resolve to the default branch, which may not yet contain this chart.
if [[ -n "${GITOPS_TARGET_REVISION:-}" ]]; then
  log "pinning git-sourced Applications to targetRevision=$GITOPS_TARGET_REVISION"
  for app in platform-config keycloak-operator platform-rgd crossplane-provider-keycloak; do
    kubectl -n argocd patch application "$app" --type merge \
      -p "{\"spec\":{\"source\":{\"targetRevision\":\"$GITOPS_TARGET_REVISION\"}}}"
  done
fi

# Wait for the core stack. inari-server / inari-console are optional until
# their charts are published by the component repos.
# Waves -3/-2 are core: KRO/Crossplane carry the platform resource
# architecture (parent plan D1/D6); cert-manager/ExternalDNS/ESO serve the
# native resources rendered hub-side.
CORE_APPS=(cert-manager external-dns external-secrets crossplane cnpg keycloak-operator kro crossplane-provider-keycloak platform-config nats openfga platform-rgd)
OPTIONAL_APPS=(inari-server inari-console)

wait_app() { # name
  log "waiting for Application/$1 to become Healthy"
  local deadline=$((SECONDS + 900)) health="" sync=""
  while (( SECONDS < deadline )); do
    health=$(kubectl -n argocd get application "$1" -o jsonpath='{.status.health.status}' 2>/dev/null || true)
    sync=$(kubectl -n argocd get application "$1" -o jsonpath='{.status.sync.status}' 2>/dev/null || true)
    if [[ "$health" == "Healthy" && "$sync" == "Synced" ]]; then
      log "Application/$1 Healthy+Synced"
      return 0
    fi
    # Degraded/Progressing are common mid-rollout; keep waiting until the
    # deadline instead of bailing on a transient state.
    sleep 5
  done
  return 1
}

for app in "${CORE_APPS[@]}"; do
  if ! wait_app "$app"; then
    kubectl -n argocd get application "$app" -o yaml | tail -40 || true
    die "core Application/$app failed to become Healthy"
  fi
done

for app in "${OPTIONAL_APPS[@]}"; do
  if ! wait_app "$app"; then
    warn "optional Application/$app not healthy yet (chart may not be published) — continuing"
  fi
done

verify_chainsaw bootstrap

log "bootstrap complete."
log "  Keycloak admin console: kubectl -n $NAMESPACE port-forward svc/keycloak-service 8080:8080"
log "  (bootstrap admin credentials from secret 'keycloak-initial-admin', keys 'username'/'password')"
log "  ArgoCD UI: kubectl -n argocd port-forward svc/argocd-server 8081:443"
