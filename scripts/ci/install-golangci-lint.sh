#!/usr/bin/env bash
set -euo pipefail

# `go install` re-verifies every transitive dependency of golangci-lint
# against sum.golang.org on each run — a large dependency tree (see
# .github/workflows/backend.yml). A single transient network error from the
# checksum database (observed: "stream error ... INTERNAL_ERROR" over HTTP/2)
# fails the whole install. Retried with backoff rather than disabling
# GOSUMDB/GONOSUMCHECK, which would drop supply-chain verification instead of
# just tolerating a flaky fetch.

PACKAGE="github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest"
MAX_ATTEMPTS=3

attempt=1
while true; do
  if go install "$PACKAGE"; then
    exit 0
  fi
  if ((attempt >= MAX_ATTEMPTS)); then
    echo "go install $PACKAGE failed after $MAX_ATTEMPTS attempts." >&2
    exit 1
  fi
  delay=$((attempt * 10))
  echo "go install $PACKAGE failed (attempt $attempt/$MAX_ATTEMPTS); retrying in ${delay}s." >&2
  sleep "$delay"
  attempt=$((attempt + 1))
done
