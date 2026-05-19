#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
OVERLAY="$ROOT_DIR/infra/k8s/overlays/k3s-dev"
RENDERED="${TMPDIR:-/tmp}/nchat-k8s-ci.yaml"

if [[ ! -f "$OVERLAY/kustomization.yaml" ]]; then
  echo "error: missing kustomization.yaml at $OVERLAY" >&2
  exit 1
fi

if command -v kubectl >/dev/null 2>&1; then
  kubectl kustomize "$OVERLAY" >"$RENDERED"
  if timeout 6s kubectl version --request-timeout=5s >/dev/null 2>&1; then
    kubectl apply --dry-run=client --validate=false -f "$RENDERED"
  else
    echo "warning: Kubernetes API is not reachable; skipped kubectl apply dry-run" >&2
  fi
elif command -v kustomize >/dev/null 2>&1; then
  kustomize build "$OVERLAY" >"$RENDERED"
  echo "warning: kubectl not found; skipped client dry-run validation" >&2
else
  echo "error: kubectl or kustomize is required to validate Kubernetes manifests" >&2
  exit 1
fi

if [[ ! -s "$RENDERED" ]]; then
  echo "error: rendered Kubernetes manifest is empty" >&2
  exit 1
fi

echo "K8s manifests CI check passed."
