#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"

if ! command -v govulncheck >/dev/null 2>&1; then
  GOVULNCHECK_FALLBACK="${GOPATH:-$HOME/go}/bin/govulncheck"
  if [[ -x "$GOVULNCHECK_FALLBACK" ]]; then
    export PATH="$(dirname "$GOVULNCHECK_FALLBACK"):$PATH"
  fi
fi

if ! command -v govulncheck >/dev/null 2>&1; then
  echo "govulncheck is not installed." >&2
  echo "Install it with:" >&2
  echo "  go install golang.org/x/vuln/cmd/govulncheck@latest" >&2
  exit 127
fi

while IFS= read -r module; do
  echo "==> govulncheck $module"
  (cd "$ROOT/$module" && govulncheck ./...)
done < <("$ROOT/scripts/ci/go-modules.sh")
