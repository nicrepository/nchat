#!/usr/bin/env bash
# The rollback, from the schema proof to the switch, under one lock (CICD-08).
#
#   NCHAT_PROD_APPLIED_LEDGER=<path> rollback-critical-section.sh --target <slot> <reason>
#
# Everything here exists because of the gap between proving a thing and acting
# on it. The schema gate answers "does the release on the target slot still fit
# the schema?", and the answer stops being true the moment a migration
# completes. Reading the ledger in one step and switching traffic in a later one
# leaves exactly that window, and no amount of checking afterwards closes it:
# once the selectors have moved, production is already serving the release the
# schema no longer supports.
#
# So the proof and the switch happen in one process, holding the migration lock
# from before the ledger is read until after the traffic has converged. The
# order is fixed and each line depends on the one above it:
#
#   1. take the lock scripts/db/migrate.sh takes -- the same id, the same
#      database -- so no migration from the deploy workflow, from a migration
#      Job, or from an operator's shell can complete while this runs;
#   2. read the ledger, fresh, INSIDE the lock. Any reading taken before this
#      point is diagnostics, not authorisation;
#   3. run the schema gate against that reading;
#   4. prove the lock session is still alive, immediately before the switch;
#   5. switch, through rollback.sh and nothing else. That script re-reads the
#      cluster, patches, reads each Service back and re-checks the target's
#      release, all still inside the lock;
#   6. prove the lock session is STILL alive. Steps 4 and 6 together are what
#      turn "a lock was taken once" into "nothing migrated between the proof and
#      the switch" -- a session that died in between would have released the lock
#      in the server, and this refuses to claim an exclusion it cannot show;
#   7. release, on every path, including refusal and signal.
#
# It runs no migration and applies no schema change. It reads the ledger, takes
# a lock, and calls rollback.sh.
set -Eeuo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
# shellcheck source=scripts/deploy/nchat-prod/lib.sh
source "$SCRIPT_DIR/lib.sh"
# shellcheck source=scripts/deploy/nchat-prod/production-mutation-lock.sh
source "$SCRIPT_DIR/production-mutation-lock.sh"
# Sourced for read_runtime_dsn; its own main does not run when sourced.
# shellcheck source=scripts/deploy/nchat-prod/applied-migrations.sh
source "$SCRIPT_DIR/applied-migrations.sh"

# Where the ledger read inside the lock is written, so the evidence step can
# publish the reading the decision was actually made from.
NCHAT_PROD_APPLIED_LEDGER="${NCHAT_PROD_APPLIED_LEDGER:-}"
TARGET_SLOT=""
ROLLBACK_REASON=""

# The ledger, read here and nowhere earlier. `main` has the lock by now, so what
# this returns cannot change again until the switch is done.
read_ledger_under_lock() {
  [[ -n "$NCHAT_PROD_APPLIED_LEDGER" ]] ||
    prod_fail "NCHAT_PROD_APPLIED_LEDGER must name the file the locked ledger read is written to"
  bash "$SCRIPT_DIR/applied-migrations.sh" >"$NCHAT_PROD_APPLIED_LEDGER"
}

run_schema_gate() {
  local release
  release="$(require_consistent_release "$TARGET_SLOT")" || return 1
  bash "$SCRIPT_DIR/rollback-schema-gate.sh" migrations "${release%%:*}" \
    "$NCHAT_PROD_APPLIED_LEDGER"
}

# Everything the lock protects, in order. Each step returns non-zero rather than
# exiting, so the caller's release runs.
protected_rollback() {
  echo "--- migration lock held; proving the schema inside it ---"
  read_ledger_under_lock || return 1
  run_schema_gate || return 1
  assert_production_mutation_lock_held "before the switch" || return 1
  echo "--- schema proved under lock; moving traffic ---"
  bash "$SCRIPT_DIR/rollback.sh" --target "$TARGET_SLOT" "$ROLLBACK_REASON" || return 1
  # After, not only before: a session that died mid-switch would have released
  # the lock in the server, and a migration could then have completed while the
  # selectors were moving. Traffic has moved by now, so this cannot prevent
  # that -- it refuses to report an exclusion that did not hold.
  assert_production_mutation_lock_held "after the switch" || return 1
  echo "--- the migration lock was held continuously across the proof and the switch ---"
}

main() {
  local dsn
  TARGET_SLOT="$(require_target_slot "$@")"
  shift 2
  ROLLBACK_REASON="${1:-}"
  [[ -n "$ROLLBACK_REASON" ]] ||
    prod_fail "usage: rollback-critical-section.sh --target <blue|green> <reason>"
  require_context
  require_namespace
  dsn="$(read_runtime_dsn)" || return 1
  with_production_mutation_lock "$dsn" protected_rollback
}

main "$@"
