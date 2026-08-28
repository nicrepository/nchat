#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"
COVERAGE_DIR="$ROOT/coverage/go"

mkdir -p "$COVERAGE_DIR"

while IFS= read -r module; do
  safe_module="${module//\//_}"
  profile="$COVERAGE_DIR/${safe_module}.out"
  summary="$COVERAGE_DIR/${safe_module}.txt"

  echo "==> go test coverage $module"
  (cd "$ROOT/$module" && go test ./... -covermode=atomic -coverprofile="$profile")

  echo "==> go tool cover $module"
  go tool cover -func="$profile" | tee "$summary"
done < <("$ROOT/scripts/ci/go-modules.sh")
