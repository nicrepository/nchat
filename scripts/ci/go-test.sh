#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"
GO_TEST_FLAGS=("$@")

while IFS= read -r module; do
  if [ "${#GO_TEST_FLAGS[@]}" -eq 0 ]; then
    echo "==> go test $module"
  else
    echo "==> go test ${GO_TEST_FLAGS[*]} $module"
  fi
  (cd "$ROOT/$module" && go test "${GO_TEST_FLAGS[@]}" ./...)
done < <("$ROOT/scripts/ci/go-modules.sh")
