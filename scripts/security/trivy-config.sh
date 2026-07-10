#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"

if ! command -v trivy >/dev/null 2>&1; then
  echo "trivy is not installed." >&2
  echo "Install it from https://aquasecurity.github.io/trivy/latest/getting-started/installation/" >&2
  exit 127
fi

# Trivy dropped --no-progress in favor of --quiet (which also suppresses
# progress bars) in newer releases. Prefer --no-progress when available so
# older Trivy installs keep their current behavior unchanged.
QUIET_FLAG="--quiet"
if trivy config --help 2>&1 | grep -q -- "--no-progress"; then
  QUIET_FLAG="--no-progress"
fi

trivy config --severity HIGH,CRITICAL --exit-code 1 "$QUIET_FLAG" "$ROOT"
