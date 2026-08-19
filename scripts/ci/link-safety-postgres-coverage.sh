#!/usr/bin/env bash
# Coverage for the RF-21 Link Safety tests that need a real PostgreSQL.
#
# Usage: link-safety-postgres-coverage.sh <go-module> [output-profile]
#
# The database is opted into with LINK_SAFETY_TEST_DATABASE_URL, which is
# deliberately neither CHAT_TEST_DATABASE_URL nor FILE_TEST_DATABASE_URL. Both
# services carry older opt-in PostgreSQL suites that are incompatible with these
# — chat-service's drop and recreate the `chat` schema on setup, while the RF-21
# tests read the schema scripts/db/migrate.sh produces including
# `files.attachments`, and file-service's upload-admission suite takes session
# advisory locks and does not finish. Exporting either variable across a whole
# `go test ./...` would run all of them together. So the DSN is exported under
# the name the chosen tests look for, for this invocation only, and the tests are
# named rather than matched by pattern: a regex wide enough to catch future RF-21
# tests is also wide enough to catch the suites above.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"
MODULE="${1:?usage: link-safety-postgres-coverage.sh <go-module> [output-profile]}"
PROFILE="${2:-$ROOT_DIR/coverage/go/${MODULE//\//_}.postgres.out}"

: "${LINK_SAFETY_TEST_DATABASE_URL:?LINK_SAFETY_TEST_DATABASE_URL is required}"

case "$MODULE" in
  services/chat-service)
    dsn_var="CHAT_TEST_DATABASE_URL"
    package="./internal/storage"
    tests=(
      TestLinkScanAdmissionPostgreSQL
      TestPendingMessageVisibilityPostgreSQL
      TestLinkSafetyStatesPostgreSQL
      TestLinkScanWorkerLifecyclePostgreSQL
      TestLinkScanCreateReplayPostgreSQL
      TestLinkReconcilePostgreSQL
      TestLinkReconcileConcurrencyPostgreSQL
      TestCrossServiceMaliciousInvalidationPostgreSQL
      TestRecordLinkVerdictMaliciousInvalidationPostgreSQL
      TestLinkReconcileConvergenceScalePostgreSQL
      TestLinkReconcileEvidenceAgePostgreSQL
      TestLinkSafetyCheckValidationDoesNotBlockWritersPostgreSQL
      TestCrossServiceReconcileLeasePostgreSQL
      TestCrossServiceTwoURLLeaseOrderPostgreSQL
      TestEditMessageIsAtomicWithLinkSafetyPostgreSQL
      TestEditMessageRechecksLockedLinkRowsPostgreSQL
      TestEditWinsThenReconcileConvergesPostgreSQL
      TestEditRemovingURLRejectsStaleRefreshPostgreSQL
      TestTwoURLConcurrentEditsUseStableLockOrderPostgreSQL
      TestMessageSecuritySnapshotsAreOneAuthorizedProjectionPostgreSQL
      TestMaliciousBodyIsWithheldFromEveryProjectionPostgreSQL
    )
    ;;
  services/file-service)
    dsn_var="FILE_TEST_DATABASE_URL"
    # The CQ-002 end-to-end proof lives with the preview service, because what it
    # asserts is that no outbound request leaves it. Both packages are named so one
    # profile still covers the whole RF-21 surface in this module.
    package="./internal/storage ./internal/linkpreview"
    tests=(
      TestLinkScanStorePostgreSQL
      TestLinkScanReconcilePostgreSQL
      TestLinkFetchDenylistPostgreSQL
      TestLinkScanReconcileEvidenceAgePostgreSQL
      TestLinkScanReconcileColumnAllowlistPostgreSQL
      TestPreviewRefusesADeniedURLPostgreSQL
      TestLinkFetchDenylistBackfillPostgreSQL
      TestLegacySafeWriterLosesToAConcurrentCondemnationPostgreSQL
    )
    ;;
  *)
    echo "No RF-21 PostgreSQL coverage is defined for $MODULE." >&2
    exit 1
    ;;
esac

# `go test -run` succeeds when a named test was renamed or removed. Refuse that
# silent coverage hole by checking every exact name before running the suite.
listed_tests="$({
  cd "$ROOT_DIR/$MODULE"
  go test $package -list '^Test'
})"
for test_name in "${tests[@]}"; do
  if ! grep -Fxq -- "$test_name" <<<"$listed_tests"; then
    echo "Required PostgreSQL test is missing from $MODULE: $test_name" >&2
    exit 1
  fi
done

run="^($(
  IFS='|'
  echo "${tests[*]}"
))\$"

mkdir -p "$(dirname "$PROFILE")"

echo "==> go test coverage RF-21 PostgreSQL $MODULE"
(
  cd "$ROOT_DIR/$MODULE"
  env "$dsn_var=$LINK_SAFETY_TEST_DATABASE_URL" \
    go test $package \
    -run "$run" \
    -v \
    -count=1 \
    -covermode=atomic \
    -coverprofile="$PROFILE"
)

# A -run that matches nothing still writes a profile, all counters zero. That is
# the failure mode a renamed test would cause, so it is refused here rather than
# surfacing later as an unexplained drop in total coverage.
if [ ! -s "$PROFILE" ] || ! awk 'NR > 1 && $3 > 0 { found = 1; exit } END { exit found ? 0 : 1 }' "$PROFILE"; then
  echo "RF-21 PostgreSQL coverage for $MODULE recorded no executed statements: ${tests[*]}" >&2
  exit 1
fi

echo "RF-21 PostgreSQL coverage written to ${PROFILE#"$ROOT_DIR"/}."
