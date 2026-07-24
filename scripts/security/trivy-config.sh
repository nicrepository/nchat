#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"
TEMPORARY="$(mktemp -d "${TMPDIR:-/tmp}/nchat-trivy-config.XXXXXX")"
trap 'rm -rf "$TEMPORARY"' EXIT

if ! command -v trivy >/dev/null 2>&1; then
  echo "trivy is not installed." >&2
  echo "Install it from https://aquasecurity.github.io/trivy/latest/getting-started/installation/" >&2
  exit 127
fi

DEFAULT_KUSTOMIZE_BIN="${RUNNER_TEMP:-${TMPDIR:-/tmp}}/nchat-dev-bin/kustomize"

if [[ -n "${KUSTOMIZE_BIN:-}" ]] && command -v "$KUSTOMIZE_BIN" >/dev/null 2>&1; then
  :
elif command -v kustomize >/dev/null 2>&1; then
  KUSTOMIZE_BIN="$(command -v kustomize)"
elif [[ -x "$DEFAULT_KUSTOMIZE_BIN" ]]; then
  KUSTOMIZE_BIN="$DEFAULT_KUSTOMIZE_BIN"
else
  echo "==> Installing checksum-verified Kustomize"
  KUSTOMIZE_DIRECTORY="$("$ROOT/scripts/deploy/nchat-dev/install-kustomize.sh")"
  KUSTOMIZE_BIN="$KUSTOMIZE_DIRECTORY/kustomize"
fi

if ! command -v "$KUSTOMIZE_BIN" >/dev/null 2>&1; then
  echo "Unable to resolve a usable Kustomize binary." >&2
  exit 127
fi

export KUSTOMIZE_BIN

echo "==> Using Kustomize: $KUSTOMIZE_BIN"
"$KUSTOMIZE_BIN" version

echo "==> Preparing rendered configuration"
"$ROOT/scripts/security/prepare-trivy-config.sh" "$TEMPORARY/input"

echo "==> Running Trivy configuration scan"
trivy config \
  --severity HIGH,CRITICAL \
  --exit-code 1 \
  --ignorefile "$ROOT/.trivyignore.yaml" \
  --timeout 15m \
  --format table \
  "$TEMPORARY/input"
