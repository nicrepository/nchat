#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
OVERLAY="${1:-infra/k8s/overlays/k3s-dev}"

if [[ "$OVERLAY" = /* ]]; then
  OVERLAY_PATH="$OVERLAY"
else
  OVERLAY_PATH="$ROOT_DIR/$OVERLAY"
fi

if [[ ! -f "$OVERLAY_PATH/kustomization.yaml" ]]; then
  echo "error: kustomization.yaml not found at $OVERLAY_PATH" >&2
  exit 1
fi

if command -v kubectl >/dev/null 2>&1; then
  kubectl kustomize "$OVERLAY_PATH"
elif command -v kustomize >/dev/null 2>&1; then
  kustomize build "$OVERLAY_PATH"
else
  echo "error: kubectl or kustomize is required to render Kubernetes manifests" >&2
  exit 1
fi
