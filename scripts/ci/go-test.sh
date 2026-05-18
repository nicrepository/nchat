#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"

while IFS= read -r module; do
  echo "==> go test $module"
  (cd "$ROOT/$module" && go test ./...)
done < <("$ROOT/scripts/ci/go-modules.sh")
