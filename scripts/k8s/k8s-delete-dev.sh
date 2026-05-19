#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
OVERLAY="$ROOT_DIR/infra/k8s/overlays/k3s-dev"

if ! command -v kubectl >/dev/null 2>&1; then
  echo "error: kubectl is required to delete k3s-dev manifests" >&2
  exit 1
fi

read -r -p "Type DELETE to remove nchat k3s-dev resources: " CONFIRMATION

if [[ "$CONFIRMATION" != "DELETE" ]]; then
  echo "Delete cancelled."
  exit 0
fi

kubectl delete -k "$OVERLAY"
