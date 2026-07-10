#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"

# `go` itself may be installed outside the shell's default PATH (e.g. a
# tarball install at /usr/local/go or a custom GOROOT). Without it,
# govulncheck fails internally and misreports "no go.mod file" even when
# run from inside a valid module directory.
if ! command -v go >/dev/null 2>&1; then
  for GO_FALLBACK_DIR in "${GOROOT:-}/bin" "/usr/local/go/bin" "/usr/lib/go/bin"; do
    if [[ -n "$GO_FALLBACK_DIR" && -x "$GO_FALLBACK_DIR/go" ]]; then
      export PATH="$GO_FALLBACK_DIR:$PATH"
      break
    fi
  done
fi

if ! command -v go >/dev/null 2>&1; then
  echo "go is not installed or not on PATH." >&2
  exit 127
fi

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
