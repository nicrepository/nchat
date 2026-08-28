#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"

if command -v gitleaks >/dev/null 2>&1; then
  gitleaks detect --source "$ROOT" --no-banner --redact
  exit 0
fi

if command -v trivy >/dev/null 2>&1; then
  trivy fs --scanners secret --exit-code 1 --no-progress "$ROOT"
  exit 0
fi

echo "gitleaks or trivy is required for local secret scanning." >&2
echo "Install one of:" >&2
echo "  gitleaks: https://github.com/gitleaks/gitleaks" >&2
echo "  trivy: https://aquasecurity.github.io/trivy/latest/getting-started/installation/" >&2
exit 127
