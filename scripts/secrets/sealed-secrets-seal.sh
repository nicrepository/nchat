#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"
UNSEALED_DIR="$ROOT_DIR/infra/k8s/secrets/unsealed"
SEALED_DIR="$ROOT_DIR/infra/k8s/secrets/sealed"

usage() {
  echo "Usage: $0 <input-secret-yaml> <output-sealed-secret-yaml> [namespace]" >&2
}

if [ "$#" -lt 2 ] || [ "$#" -gt 3 ]; then
  usage
  exit 1
fi

INPUT="$1"
OUTPUT="$2"
NAMESPACE="${3:-}"

if ! command -v kubeseal >/dev/null 2>&1; then
  echo "kubeseal is required to seal secrets." >&2
  exit 1
fi

if [ ! -f "$INPUT" ]; then
  echo "Input Secret manifest not found: $INPUT" >&2
  exit 1
fi

mkdir -p "$(dirname "$OUTPUT")"

INPUT_REAL="$(cd "$(dirname "$INPUT")" && pwd -P)/$(basename "$INPUT")"
OUTPUT_DIR_REAL="$(cd "$(dirname "$OUTPUT")" && pwd -P)"
UNSEALED_REAL="$(cd "$UNSEALED_DIR" && pwd -P)"
SEALED_REAL="$(cd "$SEALED_DIR" && pwd -P)"

case "$INPUT_REAL" in
  "$UNSEALED_REAL"/*) ;;
  *)
    read -r -p "Input is outside infra/k8s/secrets/unsealed. Type SEAL_ANYWAY to continue: " confirmation
    if [ "$confirmation" != "SEAL_ANYWAY" ]; then
      echo "Aborted." >&2
      exit 1
    fi
    ;;
esac

case "$OUTPUT_DIR_REAL" in
  "$SEALED_REAL"*) ;;
  *)
    echo "Output must be under infra/k8s/secrets/sealed." >&2
    exit 1
    ;;
esac

cmd=(kubeseal --format yaml --scope strict)
if [ -n "$NAMESPACE" ]; then
  cmd+=(--namespace "$NAMESPACE")
fi
if [ -n "${SEALED_SECRETS_CERT:-}" ]; then
  cmd+=(--cert "$SEALED_SECRETS_CERT")
fi

"${cmd[@]}" <"$INPUT" >"$OUTPUT"

if ! grep -q '^kind: SealedSecret$' "$OUTPUT"; then
  echo "kubeseal output did not produce a SealedSecret." >&2
  rm -f "$OUTPUT"
  exit 1
fi

chmod 0644 "$OUTPUT"
echo "Created SealedSecret: ${OUTPUT#$ROOT_DIR/}"
