#!/usr/bin/env bash
# The migrations production has actually applied, read from the ledger that
# records them (CICD-08).
#
#   applied-migrations.sh > applied.txt
#
# Emits one line per applied migration:
#
#   # nchat-applied-migrations v1
#   <domain>/<filename> <checksum-sha256>
#
# and exits non-zero, printing nothing usable, when it cannot establish that
# list authoritatively.
#
# Why the ledger and not the cluster. deploy.sh runs the migration Job *before*
# it applies the candidate workloads and before it waits for them, so a release
# can complete its migration and then fail to roll out: the schema is advanced
# and no Pod, Deployment or slot annotation anywhere carries that release. Every
# reading taken off the workloads reports it as never having happened, and a
# rollback gate built on one would clear a rollback across a migration it cannot
# see. public.schema_migrations is written by the migration runner itself, in
# the same transaction discipline as the migration, and is the only record in
# this system of what ran.
#
# Why not the migration Jobs. They carry the release SHA and are never deleted
# by these scripts, but infra/k8s/base/migrations/job.yaml sets
# ttlSecondsAfterFinished: 3600 — Kubernetes garbage-collects them an hour after
# they complete. Their retention cannot cover a rollback window, so a missing
# Job is indistinguishable from a migration that never ran, which is exactly the
# ambiguity a fail-closed gate must not have.
#
# It is read-only in every sense: two SELECTs, as the runtime role, over a table
# that role holds nothing but SELECT on. It runs no migration, applies no schema
# change, and writes nothing.
#
# Sourcing this file only defines functions; executing it performs the read. The
# tests drive canonical_migration directly, from the values migrate.sh itself
# produces, so the two halves of the ledger contract cannot drift apart without
# a test failing.
#
# The connection string is never an argument and never printed. It is read from
# the Secret the application services already use and handed to the client on
# standard input; psql's own diagnostics are not forwarded, because libpq puts
# the host and user it was given into them.
set -Eeuo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
# shellcheck source=scripts/deploy/nchat-prod/lib.sh
source "$SCRIPT_DIR/lib.sh"

# The header is the proof that a read happened. Without it a truncated file, an
# empty file and a file nobody wrote all look like "no migrations applied", and
# the gate would read the most dangerous state as the safest one.
NCHAT_PROD_APPLIED_HEADER='# nchat-applied-migrations v1'
# The Secret the application services take their runtime DSN from, and the
# workload that has a PostgreSQL client in it.
NCHAT_PROD_RUNTIME_SECRET="${NCHAT_PROD_RUNTIME_SECRET:-nchat-secrets}"
NCHAT_PROD_RUNTIME_DSN_KEY="${NCHAT_PROD_RUNTIME_DSN_KEY:-DATABASE_URL}"
NCHAT_PROD_PSQL_WORKLOAD="${NCHAT_PROD_PSQL_WORKLOAD:-statefulset/postgres}"

# Every applied migration, as the three columns the table really holds.
#
# Projected rather than assembled in SQL: migrate.sh persists `domain` and
# `filename` separately, and `filename` is the base name with ".up.sql" already
# stripped, so building a path here would put half of this script's contract
# into a string nobody validates. The shell owns the whole correlation instead,
# in canonical_migration.
#
# dirty and in_progress rows are not filtered out — they are refused. A dirty
# ledger means a migration stopped half-way, so what the schema contains is
# unknown, and "unknown" is the one answer a rollback gate may never round down
# to "compatible". The COUNT is taken in the same statement as the rows so the
# two cannot describe different moments.
NCHAT_PROD_LEDGER_QUERY="
SELECT CASE
         WHEN EXISTS (SELECT 1 FROM public.schema_migrations WHERE dirty OR in_progress)
           THEN 'DIRTY'
         ELSE 'CLEAN'
       END;
SELECT domain, filename, checksum_sha256
  FROM public.schema_migrations
 ORDER BY domain, filename;"

applied_fail() {
  echo "applied migrations: $*" >&2
  return 1
}

# Read without ever becoming an argument or a log line.
read_runtime_dsn() {
  local encoded
  encoded="$(kubectl get secret "$NCHAT_PROD_RUNTIME_SECRET" -n "$NCHAT_PROD_NAMESPACE" \
    -o "jsonpath={.data.$NCHAT_PROD_RUNTIME_DSN_KEY}" 2>/dev/null)" ||
    applied_fail "cannot read secret/$NCHAT_PROD_RUNTIME_SECRET in $NCHAT_PROD_NAMESPACE; the identity running this needs get on that Secret" ||
    return 1
  [[ -n "$encoded" ]] ||
    applied_fail "secret/$NCHAT_PROD_RUNTIME_SECRET has no $NCHAT_PROD_RUNTIME_DSN_KEY" || return 1
  printf '%s' "$encoded" | base64 -d 2>/dev/null ||
    applied_fail "the $NCHAT_PROD_RUNTIME_DSN_KEY in secret/$NCHAT_PROD_RUNTIME_SECRET is not valid base64"
}

# One SELECT, as the runtime role, with the connection string arriving on stdin.
#
# psql's stderr is deliberately dropped rather than forwarded: libpq puts the
# host and user of the connection it was handed into its own error text, and
# this output is a public run log. The exit status is the diagnosis, and the
# runbook carries the command to run by hand.
query_ledger() {
  local dsn="$1"
  printf '%s\n' "$dsn" | kubectl exec -i "$NCHAT_PROD_PSQL_WORKLOAD" \
    -n "$NCHAT_PROD_NAMESPACE" -- \
    sh -c 'read -r dsn; exec psql "$dsn" -X -q -A -t -F " " -v ON_ERROR_STOP=1 -c "$0"' \
    "$NCHAT_PROD_LEDGER_QUERY" 2>/dev/null
}

# One row, as the database really stores it, turned into the one form the schema
# gate consumes: "<domain>/<base>.up.sql <checksum>".
#
# This is the ONLY place ".up.sql" is added. migrate.sh strips it before it
# persists the name -- parse_up_file does MFILE="${filename%.up.sql}" -- so the
# row reads "chat 000100_pin <sha>" while the file it refers to is
# migrations/chat/000100_pin.up.sql. Spreading that reconstruction across the
# query, the parser, the gate and the fixtures is what let the two formats drift
# apart in the first place.
#
# Each field is checked against migrate.sh's own validators rather than a looser
# pattern invented here, so what is accepted is exactly what that script can
# have written: domain ^[a-z][a-z0-9_]*$, filename ^[0-9]{6}_[a-z0-9_]+$,
# checksum 64 hex. A name that already carries ".up.sql" fails on the dot, one
# holding a slash, ".." or whitespace fails on those, a domain that is not a
# domain fails, and a row with one field too many fails on the remainder. None
# of them is something migrate.sh produces, and none is quietly normalised into
# something that looks like it.
canonical_migration() {
  local row="$1" domain filename checksum extra
  read -r domain filename checksum extra <<<"$row"
  [[ -z "$extra" ]] &&
    [[ "$domain" =~ ^[a-z][a-z0-9_]*$ ]] &&
    [[ "$filename" =~ ^[0-9]{6}_[a-z0-9_]+$ ]] &&
    [[ "$checksum" =~ ^[a-f0-9]{64}$ ]] ||
    applied_fail "the ledger holds a row this cannot read: $row" || return 1
  printf '%s/%s.up.sql %s' "$domain" "$filename" "$checksum"
}

# The whole ledger, or nothing at all.
#
# Every row is validated before a single byte reaches stdout. Streaming the
# header and the rows as they were read would leave a partial ledger behind when
# row seventeen turned out to be unreadable -- and a partial ledger carrying a
# valid header is exactly the shape the gate accepts as "this is everything
# production has applied". Evidence that stops half-way is worse than no
# evidence, because only one of the two is recognisable as missing.
#
# The rows are held in an array rather than a temporary file: nothing to name,
# nothing to clean up, and no window in which a half-written file exists.
parse_ledger() {
  local output="$1" state line canonical
  local -a rows=()
  state="$(head -n 1 <<<"$output")"
  [[ "$state" == CLEAN ]] ||
    applied_fail "the migration ledger reports $state; a half-applied migration means the schema is unknown" ||
    return 1
  while IFS= read -r line; do
    [[ -n "$line" ]] || continue
    # Assigned on its own line, so a refusal ends the read instead of becoming
    # an empty element in the middle of the evidence.
    canonical="$(canonical_migration "$line")" || return 1
    rows+=("$canonical")
  done < <(tail -n +2 <<<"$output")
  printf '%s\n' "$NCHAT_PROD_APPLIED_HEADER"
  [[ "${#rows[@]}" -eq 0 ]] || printf '%s\n' "${rows[@]}"
}

main() {
  local dsn output
  require_context
  require_namespace
  dsn="$(read_runtime_dsn)" || return 1
  output="$(query_ledger "$dsn")" ||
    applied_fail "the migration ledger could not be read from $NCHAT_PROD_PSQL_WORKLOAD; the rollback cannot be proved safe" ||
    return 1
  parse_ledger "$output"
}

if [[ "${BASH_SOURCE[0]}" == "${0}" ]]; then
  main "$@"
fi
