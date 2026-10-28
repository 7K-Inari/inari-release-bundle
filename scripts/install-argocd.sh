#!/usr/bin/env bash
# Installs ArgoCD onto the current cluster (day-0 prerequisite for the
# gitops/ layout). Idempotent.
set -euo pipefail

ARGOCD_VERSION="${ARGOCD_VERSION:-v3.1.5}"

log() { printf '\033[1;34m[argocd]\033[0m %s\n' "$*"; }

command -v kubectl >/dev/null || { echo "kubectl required" >&2; exit 1; }

if kubectl get crd applications.argoproj.io >/dev/null 2>&1; then
  log "ArgoCD CRDs present — skipping install"
else
  log "installing ArgoCD $ARGOCD_VERSION"
  kubectl create namespace argocd --dry-run=client -o yaml | kubectl apply -f -
  kubectl apply -n argocd --server-side --force-conflicts \
    -f "https://raw.githubusercontent.com/argoproj/argo-cd/$ARGOCD_VERSION/manifests/install.yaml"
fi

# Upstream sets imagePullPolicy=Always on argocd-redis, which bypasses node
# image caches and re-pulls public.ecr.aws on every pod start — ECR Public's
# anonymous data quota is routinely exhausted on shared CI egress IPs
# (ImagePullBackOff), and the whole ArgoCD stack degrades without redis.
# IfNotPresent uses the preloaded image when present (CI side-loads it) and
# pulls once otherwise.
if kubectl -n argocd get deployment argocd-redis >/dev/null 2>&1; then
  kubectl -n argocd patch deployment argocd-redis --type merge -p \
    '{"spec":{"template":{"spec":{"containers":[{"name":"redis","imagePullPolicy":"IfNotPresent"}]}}}}'
fi

log "waiting for argocd-server"
kubectl -n argocd rollout status deployment/argocd-server --timeout=300s
kubectl -n argocd rollout status deployment/argocd-repo-server --timeout=300s

# Optional: registry credentials for the OCI charts consumed from
# oci://ghcr.io/7k-inari/<repo>/charts. The chart packages are public, so ArgoCD
# pulls anonymously; this secret is only needed while a package is still
# private (e.g. before the first post-move release flips visibility).
if [[ -n "${GHCR_TOKEN:-}" ]]; then
  log "configuring ArgoCD repo credentials for ghcr.io OCI charts"
  kubectl -n argocd apply -f - <<EOF
apiVersion: v1
kind: Secret
metadata:
  name: ghcr-oci-repo
  namespace: argocd
  labels:
    argocd.argoproj.io/secret-type: repository
stringData:
  type: helm
  name: inari-charts
  url: ghcr.io/7k-inari
  enableOCI: "true"
  username: ${GHCR_USERNAME:-github}
  password: ${GHCR_TOKEN}
EOF
fi
log "ArgoCD ready"
