#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"
GO_TEST_FLAGS=("$@")

# shellcheck source=lib/go-retry.sh
source "$ROOT/scripts/ci/lib/go-retry.sh"

while IFS= read -r module; do
  if [ "${#GO_TEST_FLAGS[@]}" -eq 0 ]; then
    echo "==> go test $module"
  else
    echo "==> go test ${GO_TEST_FLAGS[*]} $module"
  fi
  run_go_test() { (cd "$ROOT/$module" && go test "${GO_TEST_FLAGS[@]}" ./...); }
  go_retry run_go_test
done < <("$ROOT/scripts/ci/go-modules.sh")
