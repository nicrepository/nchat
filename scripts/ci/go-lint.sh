#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"

if ! command -v golangci-lint >/dev/null 2>&1; then
  echo "golangci-lint is not installed." >&2
  echo "Install it with:" >&2
  echo "  go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest" >&2
  exit 127
fi

while IFS= read -r module; do
  echo "==> golangci-lint $module"
  (cd "$ROOT/$module" && golangci-lint run --config "$ROOT/.golangci.yml" ./...)
done < <("$ROOT/scripts/ci/go-modules.sh")
