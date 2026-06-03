#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"

while IFS= read -r module; do
  echo "==> gofmt check $module"
  mapfile -t files < <(find "$ROOT/$module" -name "*.go" -type f -print | sort)

  if [ "${#files[@]}" -eq 0 ]; then
    continue
  fi

  unformatted="$(gofmt -l "${files[@]}")"
  if [ -n "$unformatted" ]; then
    echo "Go files are not gofmt-formatted:"
    echo "$unformatted"
    exit 1
  fi
done < <("$ROOT/scripts/ci/go-modules.sh")
