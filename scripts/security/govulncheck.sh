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
  echo "  go install golang.org/x/vuln/cmd/govulncheck@v1.1.4" >&2
  exit 127
fi

# Reports are collected rather than judged one at a time: whether a finding is
# acceptable is a repository-wide question (.govulncheckignore.yaml), so the
# verdict belongs to the gate below and not to govulncheck's own exit code.
REPORT_DIR="$(mktemp -d)"
trap 'rm -rf "$REPORT_DIR"' EXIT

while IFS= read -r module; do
  echo "==> govulncheck $module"
  report="$REPORT_DIR/$(printf '%s' "$module" | tr '/' '_').json"
  # Exit 3 is "vulnerabilities found", which is a verdict for the gate to
  # reach, not a reason to stop scanning the remaining modules. Anything else
  # non-zero is govulncheck itself failing, and that still aborts here: a scan
  # that did not run must never read as a scan that found nothing.
  set +e
  (cd "$ROOT/$module" && govulncheck -format json ./...) >"$report"
  status=$?
  set -e
  if [[ "$status" -ne 0 && "$status" -ne 3 ]]; then
    echo "govulncheck failed to scan $module (exit $status)" >&2
    exit "$status"
  fi
done < <("$ROOT/scripts/ci/go-modules.sh")

python3 "$ROOT/scripts/security/govulncheck_gate.py" "$REPORT_DIR"/*.json
