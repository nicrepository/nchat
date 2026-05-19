#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"

mapfile -t files < <(find "$ROOT/services" "$ROOT/libs/go" -name "*.go" -type f -print | sort)

if [ "${#files[@]}" -eq 0 ]; then
  exit 0
fi

gofmt -w "${files[@]}"
