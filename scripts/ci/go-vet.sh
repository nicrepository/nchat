#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"

while IFS= read -r module; do
  echo "==> go vet $module"
  (cd "$ROOT/$module" && go vet ./...)
done < <("$ROOT/scripts/ci/go-modules.sh")
