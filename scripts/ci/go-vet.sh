#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"

# shellcheck source=lib/go-retry.sh
source "$ROOT/scripts/ci/lib/go-retry.sh"

while IFS= read -r module; do
  echo "==> go vet $module"
  run_go_vet() { (cd "$ROOT/$module" && go vet ./...); }
  go_retry run_go_vet
done < <("$ROOT/scripts/ci/go-modules.sh")
