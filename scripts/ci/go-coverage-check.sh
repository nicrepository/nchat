#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"
COVERAGE_DIR="$ROOT_DIR/coverage/go"
THRESHOLD="${GO_COVERAGE_THRESHOLD:-90}"

# shellcheck source=lib/go-retry.sh
source "$ROOT_DIR/scripts/ci/lib/go-retry.sh"

mkdir -p "$COVERAGE_DIR"

echo "Go coverage threshold: ${THRESHOLD}%"

while IFS= read -r module; do
  safe_module="${module//\//_}"
  profile="$COVERAGE_DIR/${safe_module}.threshold.out"
  summary="$COVERAGE_DIR/${safe_module}.threshold.txt"

  mapfile -t packages < <(
    cd "$ROOT_DIR/$module"
    go list ./... | awk '$0 !~ /\/cmd(\/|$)/ { print }'
  )

  if [ "${#packages[@]}" -eq 0 ]; then
    echo "==> go coverage threshold $module"
    echo "No non-cmd packages found for coverage threshold in $module." >&2
    exit 1
  fi

  echo "==> go test coverage threshold $module"
  run_go_test_coverage() {
    (cd "$ROOT_DIR/$module" && go test "${packages[@]}" -covermode=atomic -coverprofile="$profile")
  }
  go_retry run_go_test_coverage

  if [ ! -s "$profile" ]; then
    echo "Coverage profile is empty for $module." >&2
    exit 1
  fi

  # The RF-21 Link Safety tests only run against a real PostgreSQL, so the run
  # above skips them and the code they exercise counts as uncovered. When a
  # database is offered they are measured separately and merged in, and the same
  # threshold then applies to the whole. See the helper for why this DSN is not
  # exported into the run above.
  case "$module" in
    services/chat-service | services/file-service)
      if [ -n "${LINK_SAFETY_TEST_DATABASE_URL:-}" ]; then
        postgres_profile="$COVERAGE_DIR/${safe_module}.postgres.out"
        merged_profile="$COVERAGE_DIR/${safe_module}.merged.out"

        "$ROOT_DIR/scripts/ci/link-safety-postgres-coverage.sh" "$module" "$postgres_profile"

        echo "==> merge coverage profiles $module"
        awk -f "$ROOT_DIR/scripts/ci/merge-go-coverprofiles.awk" \
          "$profile" "$postgres_profile" >"$merged_profile"
        mv "$merged_profile" "$profile"
      fi
      ;;
  esac

  echo "==> go tool cover $module"
  go tool cover -func="$profile" | tee "$summary"

  coverage="$(
    awk '/^total:/ {
      value = $3
      sub(/%$/, "", value)
      print value
    }' "$summary"
  )"

  if [ -z "$coverage" ]; then
    echo "Could not determine total coverage for $module." >&2
    exit 1
  fi

  if ! awk -v coverage="$coverage" -v threshold="$THRESHOLD" 'BEGIN { exit (coverage + 0 >= threshold + 0) ? 0 : 1 }'; then
    echo "Go coverage for $module is ${coverage}%, below threshold ${THRESHOLD}%." >&2
    exit 1
  fi

  echo "Go coverage for $module is ${coverage}%."
done < <("$ROOT_DIR/scripts/ci/go-modules.sh")

echo "Go coverage threshold passed."
