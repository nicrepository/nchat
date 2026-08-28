#!/usr/bin/env bash
set -Eeuo pipefail

{
  if [[ -n "${MIGRATIONS_POST_UP_SQL_FILE:-}" ]]; then
    printf '%s\n' 'hook=set'
  else
    printf '%s\n' 'hook=unset'
  fi
  for argument in "$@"; do
    printf 'arg=%s\n' "$argument"
  done
} >"$FAKE_RECORD"
exit "${FAKE_EXIT:-0}"
