#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
OVERLAY="$ROOT_DIR/infra/k8s/overlays/k3s-dev"

if ! command -v kubectl >/dev/null 2>&1; then
  echo "error: kubectl is required to apply k3s-dev manifests" >&2
  exit 1
fi

kubectl apply -k "$OVERLAY"

echo "Applied NChat k3s-dev manifests."
echo "Next commands:"
echo "  make k8s-status-dev"
echo "  curl -H 'Host: nchat.local' http://127.0.0.1/"
