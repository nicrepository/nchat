#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
OVERLAY="${1:-infra/k8s/overlays/k3s-dev}"
RENDERED="${TMPDIR:-/tmp}/nchat-k8s-rendered.yaml"

"$ROOT_DIR/scripts/k8s/k8s-render.sh" "$OVERLAY" >"$RENDERED"

if [[ ! -s "$RENDERED" ]]; then
  echo "error: rendered Kubernetes manifest is empty" >&2
  exit 1
fi

if command -v kubectl >/dev/null 2>&1; then
  if timeout 6s kubectl version --request-timeout=5s >/dev/null 2>&1; then
    kubectl apply --dry-run=client --validate=false -f "$RENDERED"
  else
    echo "warning: Kubernetes API is not reachable; skipped kubectl apply dry-run" >&2
  fi
else
  echo "warning: kubectl not found; skipped client dry-run validation" >&2
fi

if command -v kubeconform >/dev/null 2>&1; then
  kubeconform -strict -summary "$RENDERED"
else
  echo "info: kubeconform not found; skipped schema validation" >&2
fi

echo "K8s manifests validation passed."
