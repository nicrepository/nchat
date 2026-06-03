#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"
COVERAGE_DIR="$ROOT_DIR/coverage/go"
THRESHOLD="${GO_COVERAGE_THRESHOLD:-90}"

mkdir -p "$COVERAGE_DIR"

echo "Go coverage threshold: ${THRESHOLD}%"

while IFS= read -r module; do
  safe_module="${module//\//_}"
  profile="$COVERAGE_DIR/${safe_module}.threshold.out"
  summary="$COVERAGE_DIR/${safe_module}.threshold.txt"

  mapfile -t packages < <(
    cd "$ROOT_DIR/$module"
    go list ./... | awk '$0 !~ /\/cmd(\/|$)/ { print }'
  )

  if [ "${#packages[@]}" -eq 0 ]; then
    echo "==> go coverage threshold $module"
    echo "No non-cmd packages found for coverage threshold in $module." >&2
    exit 1
  fi

  echo "==> go test coverage threshold $module"
  (cd "$ROOT_DIR/$module" && go test "${packages[@]}" -covermode=atomic -coverprofile="$profile")

  if [ ! -s "$profile" ]; then
    echo "Coverage profile is empty for $module." >&2
    exit 1
  fi

  echo "==> go tool cover $module"
  go tool cover -func="$profile" | tee "$summary"

  coverage="$(
    awk '/^total:/ {
      value = $3
      sub(/%$/, "", value)
      print value
    }' "$summary"
  )"

  if [ -z "$coverage" ]; then
    echo "Could not determine total coverage for $module." >&2
    exit 1
  fi

  if ! awk -v coverage="$coverage" -v threshold="$THRESHOLD" 'BEGIN { exit (coverage + 0 >= threshold + 0) ? 0 : 1 }'; then
    echo "Go coverage for $module is ${coverage}%, below threshold ${THRESHOLD}%." >&2
    exit 1
  fi

  echo "Go coverage for $module is ${coverage}%."
done < <("$ROOT_DIR/scripts/ci/go-modules.sh")

echo "Go coverage threshold passed."
