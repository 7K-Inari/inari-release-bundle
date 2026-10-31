#!/usr/bin/env bash
# DR drill — the M0 exit-gate artifact (plan §9 M0, §12.1/1).
#
# 1. Provisions a FRESH kind cluster (separate from the dev cluster).
# 2. Bootstraps the gitops stack (ArgoCD + gitops/ Applications).
# 3. Restores from a given backup tarball.
# 4. Verifies the restored state with the shared smoke checks.
#
# Usage: scripts/dr-drill.sh <backup.tgz> [--skip-kind] [--keep]

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib/common.sh
source "$SCRIPT_DIR/lib/common.sh"

KEEP=false
SKIP_KIND=false
ARCHIVE=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --keep) KEEP=true; shift ;;
    --skip-kind) SKIP_KIND=true; shift ;;
    -*) die "unknown option: $1" ;;
    *)
      if [[ -n "$ARCHIVE" ]]; then
        die "usage: dr-drill.sh <backup.tgz> [--skip-kind] [--keep]"
      fi
      ARCHIVE="$1"; shift ;;
  esac
done
[[ -n "$ARCHIVE" ]] || die "usage: dr-drill.sh <backup.tgz> [--skip-kind] [--keep]"
[[ -f "$ARCHIVE" ]] || die "backup archive not found: $ARCHIVE"
ARCHIVE="$(cd "$(dirname "$ARCHIVE")" && pwd)/$(basename "$ARCHIVE")"

DR_CLUSTER="${DR_CLUSTER_NAME:-inari-dr}"
export KIND_CLUSTER_NAME="$DR_CLUSTER"

require kind kubectl jq

cleanup() {
  if ! $KEEP; then
    log "tearing down DR kind cluster '$DR_CLUSTER'"
    kind delete cluster --name "$DR_CLUSTER" >/dev/null 2>&1 || true
  fi
}
trap cleanup EXIT

if $SKIP_KIND; then
  log "using existing DR kind cluster '$DR_CLUSTER' (--skip-kind)"
  kubectl config use-context "kind-${DR_CLUSTER}" >/dev/null
else
  log "provisioning fresh DR kind cluster '$DR_CLUSTER'"
  kind get clusters 2>/dev/null | grep -qx "$DR_CLUSTER" \
    && die "cluster '$DR_CLUSTER' already exists; delete it first (kind delete cluster --name $DR_CLUSTER)"
  kind create cluster --name "$DR_CLUSTER"
  kubectl config use-context "kind-${DR_CLUSTER}" >/dev/null

  # Private operator image can't be pulled anonymously from kind.
  if [[ -n "${GHCR_TOKEN:-}" ]]; then
    "$SCRIPT_DIR/preload-operator-image.sh" "$DR_CLUSTER"
  fi
fi

log "bootstrapping the gitops stack on the DR cluster"
"$SCRIPT_DIR/bootstrap.sh" --skip-kind

log "restoring backup $ARCHIVE"
"$SCRIPT_DIR/restore.sh" "$ARCHIVE"

log "verifying restored state"
verify_chainsaw bootstrap dr

log "DR DRILL PASSED: backup restored and verified on a fresh cluster"
if $KEEP; then
  log "cluster kept: kubectl config use-context kind-${DR_CLUSTER}"
fi
