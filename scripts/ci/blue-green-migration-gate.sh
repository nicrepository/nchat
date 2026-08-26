#!/usr/bin/env bash
# Blue/Green expand-contract gate for SQL migrations (issue #626).
#
# Production runs two release slots against one database, and the slot that was
# already live has to keep working after the candidate's migration is applied —
# that is the only reason rollback can be a selector change instead of a
# redeploy. So an up migration may expand the schema freely, but an operation
# that takes something away from code that is still running is a contract-phase
# operation and belongs in a later release, once no slot depends on the old
# shape.
#
# A contract-phase operation is not forbidden, it is required to be deliberate.
# A NEW migration declares it in the file itself:
#
#     -- nchat:blue-green contract-phase <why this is safe now>
#
# An ALREADY APPLIED migration is never edited to satisfy this gate. The runner
# stores a SHA-256 of the whole file (scripts/db/migrate.sh migration_checksum)
# and refuses to continue when it changes, so adding a comment to a historical
# migration would block every deployment that has already run it. Migrations
# written before this policy are listed in the exceptions file beside this
# script instead.
#
# Deliberately NOT a SQL parser. It is a conservative pattern gate over the
# statement stream, and it is honest about its edges:
#   - dynamic SQL built inside DO $$ ... $$ is matched only when the dangerous
#     keywords appear literally;
#   - a CREATE INDEX that locks the table for a long time is not detected here,
#     because CONCURRENTLY cannot run inside a transaction and every migration
#     in this repository is required to be transactional. That trade-off is a
#     review checklist item in the runbook, not a rule that would contradict an
#     existing one.
#
# Sourcing this file only defines functions; executing it runs the gate.

BLUE_GREEN_GATE_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
BLUE_GREEN_EXCEPTIONS_FILE="${BLUE_GREEN_EXCEPTIONS_FILE:-$BLUE_GREEN_GATE_DIR/blue-green-migration-exceptions.txt}"

# The operations that can break a slot running the previous release. Returns the
# operation's name on stdout, or 1 when the statement is compatible.
blue_green_incompatible_operation() {
  local statement="$1" operation=""
  shopt -s nocasematch
  if [[ "$statement" =~ DROP[[:space:]]+(TABLE|COLUMN|TYPE|CONSTRAINT|DEFAULT) ]]; then
    operation="DROP ${BASH_REMATCH[1]^^}"
  elif [[ "$statement" =~ RENAME[[:space:]]+(VALUE|COLUMN|CONSTRAINT|ATTRIBUTE|TO) ]]; then
    operation="RENAME ${BASH_REMATCH[1]^^}"
  elif [[ "$statement" =~ SET[[:space:]]+NOT[[:space:]]+NULL ]]; then
    operation="SET NOT NULL"
  elif [[ "$statement" =~ ALTER[[:space:]]+COLUMN[[:space:]]+[a-zA-Z0-9_\"]+[[:space:]]+(SET[[:space:]]+DATA[[:space:]]+)?TYPE ]]; then
    operation="column type change"
  fi
  shopt -u nocasematch
  [ -n "$operation" ] || return 1
  printf '%s' "$operation"
}

blue_green_has_contract_marker() {
  grep -qiE '^[[:space:]]*--[[:space:]]*nchat:blue-green[[:space:]]+contract-phase[[:space:]]+[^[:space:]]' "$1"
}

# "<domain>/<filename>" — the key the exceptions file uses.
blue_green_migration_key() {
  local file="$1"
  printf '%s/%s' "$(basename "$(dirname "$file")")" "$(basename "$file")"
}

blue_green_is_pre_policy() {
  local key
  [ -f "$BLUE_GREEN_EXCEPTIONS_FILE" ] || return 1
  key="$(blue_green_migration_key "$1")"
  grep -v '^[[:space:]]*#' "$BLUE_GREEN_EXCEPTIONS_FILE" | grep -Fxq "$key"
}

# Splits a file into SQL statements and reports every incompatible one.
# Prints "<key>: <operation>" per finding; returns 1 when there is at least one.
blue_green_scan_file() {
  local file="$1" statement="" line clean current operation found=1
  while IFS= read -r line || [[ -n "$line" ]]; do
    clean="${line%%--*}"
    statement+=" $clean"
    while [[ "$statement" == *";"* ]]; do
      current="${statement%%;*};"
      statement="${statement#*;}"
      if operation="$(blue_green_incompatible_operation "$current")"; then
        printf '%s: %s\n' "$(blue_green_migration_key "$file")" "$operation"
        found=0
      fi
    done
  done <"$file"
  if operation="$(blue_green_incompatible_operation "$statement")"; then
    printf '%s: %s\n' "$(blue_green_migration_key "$file")" "$operation"
    found=0
  fi
  return "$found"
}

# blue_green_check_file <file> -> 0 when the migration is acceptable.
# Diagnostics go to stderr so a caller can report them verbatim.
blue_green_check_file() {
  local file="$1" findings
  if blue_green_is_pre_policy "$file"; then
    return 0
  fi
  findings="$(blue_green_scan_file "$file")" || return 0
  if blue_green_has_contract_marker "$file"; then
    return 0
  fi
  printf '%s\n' "$findings" >&2
  return 1
}

blue_green_gate_main() {
  local migrations_dir="${1:?usage: blue-green-migration-gate.sh <migrations-dir>}" file failures=0
  local -a up_files=()
  mapfile -t up_files < <(find "$migrations_dir" -name '*.up.sql' | LC_ALL=C sort)
  for file in "${up_files[@]}"; do
    blue_green_check_file "$file" || failures=$((failures + 1))
  done
  [ "$failures" -eq 0 ] || return 1
}

if [[ "${BASH_SOURCE[0]}" == "${0}" ]]; then
  set -Eeuo pipefail
  blue_green_gate_main "$@"
fi
