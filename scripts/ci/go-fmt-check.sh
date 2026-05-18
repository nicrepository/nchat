#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"

mapfile -t files < <(find "$ROOT/services" "$ROOT/libs/go" -name "*.go" -print | sort)

if [ "${#files[@]}" -eq 0 ]; then
  exit 0
fi

unformatted="$(gofmt -l "${files[@]}")"
if [ -n "$unformatted" ]; then
  echo "Go files are not gofmt-formatted:"
  echo "$unformatted"
  exit 1
fi
