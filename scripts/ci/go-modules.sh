#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"

find "$ROOT/libs/go" "$ROOT/services" -name go.mod -type f -print \
  | while IFS= read -r modfile; do
    rel="${modfile#"$ROOT/"}"
    printf '%s\n' "${rel%/go.mod}"
  done \
  | sort
