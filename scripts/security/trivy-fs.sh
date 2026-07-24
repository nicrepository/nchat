#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"

if ! command -v trivy >/dev/null 2>&1; then
  echo "trivy is not installed." >&2
  echo "Install it from https://aquasecurity.github.io/trivy/latest/getting-started/installation/" >&2
  exit 127
fi

trivy fs \
  --scanners vuln \
  --severity HIGH,CRITICAL \
  --exit-code 1 \
  --no-progress \
  --timeout 15m \
  "$ROOT"
