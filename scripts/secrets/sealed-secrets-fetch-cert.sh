#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"
OUT_DIR="$ROOT_DIR/infra/k8s/secrets/public-certs"

if ! command -v kubectl >/dev/null 2>&1; then
  echo "kubectl is required to resolve the current context." >&2
  exit 1
fi

if ! command -v kubeseal >/dev/null 2>&1; then
  echo "kubeseal is required to fetch the controller public certificate." >&2
  exit 1
fi

CONTEXT="$(kubectl config current-context 2>/dev/null || true)"
if [ -z "$CONTEXT" ]; then
  echo "No current kubectl context found." >&2
  exit 1
fi

SAFE_CONTEXT="$(printf '%s' "$CONTEXT" | tr -c 'A-Za-z0-9_.-' '-')"
OUT_FILE="$OUT_DIR/sealed-secrets-${SAFE_CONTEXT}.pem"
mkdir -p "$OUT_DIR"

kubeseal --fetch-cert >"$OUT_FILE"
chmod 0644 "$OUT_FILE"

echo "Fetched Sealed Secrets public certificate: ${OUT_FILE#$ROOT_DIR/}"
echo "Public certificate cache is ignored by Git by default."
