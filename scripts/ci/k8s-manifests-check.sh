#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
RENDERED_DIR="$(mktemp -d "${TMPDIR:-/tmp}/nchat-k8s-ci.XXXXXX")"

cleanup() {
  rm -rf "$RENDERED_DIR"
}
trap cleanup EXIT

if [ -n "${K8S_OVERLAY:-}" ]; then
  overlays=("$K8S_OVERLAY")
else
  overlays=(
    infra/k8s/overlays/k3s-dev
    infra/k8s/overlays/k3s-staging
  )
fi

render_overlay() {
  local overlay="$1"
  local overlay_path="$overlay"
  if [[ "$overlay_path" != /* ]]; then
    overlay_path="$ROOT_DIR/$overlay_path"
  fi

  if [[ ! -f "$overlay_path/kustomization.yaml" ]]; then
    echo "error: missing kustomization.yaml at $overlay_path" >&2
    exit 1
  fi

  local rendered="$RENDERED_DIR/$(basename "$overlay_path").yaml"

  if command -v kubectl >/dev/null 2>&1; then
    kubectl kustomize "$overlay_path" >"$rendered"
    if timeout 6s kubectl version --request-timeout=5s >/dev/null 2>&1; then
      kubectl apply --dry-run=client --validate=false -f "$rendered"
    else
      echo "warning: Kubernetes API is not reachable; skipped kubectl apply dry-run for $overlay" >&2
    fi
  elif command -v kustomize >/dev/null 2>&1; then
    kustomize build "$overlay_path" >"$rendered"
    echo "warning: kubectl not found; skipped client dry-run validation for $overlay" >&2
  else
    echo "error: kubectl or kustomize is required to validate Kubernetes manifests" >&2
    exit 1
  fi

  if [[ ! -s "$rendered" ]]; then
    echo "error: rendered Kubernetes manifest is empty for $overlay" >&2
    exit 1
  fi

  if command -v kubeconform >/dev/null 2>&1; then
    kubeconform -strict -ignore-missing-schemas -summary "$rendered"
  else
    echo "info: kubeconform not found; skipped schema validation for $overlay" >&2
  fi
}

for overlay in "${overlays[@]}"; do
  render_overlay "$overlay"
done

echo "K8s manifests CI check passed."
