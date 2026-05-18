#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"

find "$ROOT/services" "$ROOT/libs/go" -name go.mod -print \
  | while IFS= read -r modfile; do
    rel="${modfile#"$ROOT/"}"
    printf '%s\n' "${rel%/go.mod}"
  done \
  | sort
