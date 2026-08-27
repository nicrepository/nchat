#!/usr/bin/env bash
# Proves the production backup and restore procedure keeps least privilege.
#
#   scripts/db/postgres-restore-test.sh
#
# The failure this exists to catch is silent and late. A dump restored by the
# admin role leaves every table owned by the admin; nchat_migrator then cannot
# ALTER them, so the next migration fails -- and grant-runtime.sql fails with
# it, which takes nchat_app's access down too. Nothing notices at restore time.
# The database looks complete and is unmaintainable.
#
# So this walks the whole contract on a real PostgreSQL: bootstrap the roles,
# migrate as nchat_migrator, dump, restore into an empty database, and then
# require that migrations still apply and that nchat_app still has exactly the
# access it is supposed to have and no more.
#
# Requires Docker and nothing else -- it starts and removes its own container,
# needs no compose stack and no cluster, which is why it can be run on a laptop
# before a release. It is deliberately NOT in `pnpm ci`: the same reason
# migrations-smoke is not, a CI runner is not guaranteed a Docker daemon.
set -Eeuo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"
BOOTSTRAP="$ROOT_DIR/infra/k8s/overlays/k3s-prod/stateful/postgres-bootstrap.sh"
GRANTS="$ROOT_DIR/scripts/db/grant-runtime.sql"
MIGRATIONS="$ROOT_DIR/migrations"
# The image the production stateful layer pins, so the test exercises the
# version production actually runs -- extension trust rules in particular are a
# property of the server version.
IMAGE="${NCHAT_RESTORE_TEST_IMAGE:-postgres:16.10-alpine}"
CONTAINER="nchat-restore-test-$$"
# Test-local credentials for a throwaway container that is never published.
# Production's come from Secrets and are never handled by this script.
ADMIN_USER=nchat_admin_test
ADMIN_PASSWORD=test-admin-password
MIGRATOR_PASSWORD=test-migrator-password
APP_PASSWORD=test-app-password
SCHEMAS=(auth chat files)

ERRORS=0
fail() { echo "  [FAIL] $*" >&2; ERRORS=$((ERRORS + 1)); }
ok() { echo "  [OK]   $*"; }

cleanup() { docker rm -f "$CONTAINER" >/dev/null 2>&1 || true; }
trap cleanup EXIT

# Passwords travel as environment variables into the container, never as
# command-line arguments, so they stay out of the host's and the container's
# process listings.
as_admin() {
  docker exec -e PGPASSWORD="$ADMIN_PASSWORD" "$CONTAINER" \
    psql -U "$ADMIN_USER" -h 127.0.0.1 -d nchat -q -v ON_ERROR_STOP=1 "$@"
}
# client_min_messages=warning silences the "already exists, skipping" notices
# the idempotent migrations emit; a real error still stops the run through
# ON_ERROR_STOP.
as_migrator() {
  docker exec -e PGPASSWORD="$MIGRATOR_PASSWORD" \
    -e PGOPTIONS="-c client_min_messages=warning" "$CONTAINER" \
    psql -U nchat_migrator -h 127.0.0.1 -d nchat -q -v ON_ERROR_STOP=1 "$@"
}
as_app() {
  docker exec -e PGPASSWORD="$APP_PASSWORD" "$CONTAINER" \
    psql -U nchat_app -h 127.0.0.1 -d nchat -q -v ON_ERROR_STOP=1 "$@"
}
scalar() { as_admin -tA -c "$1" | tr -d '\r'; }

start_postgres() {
  echo "--- postgres ---"
  docker run -d --name "$CONTAINER" \
    -e POSTGRES_USER="$ADMIN_USER" \
    -e POSTGRES_PASSWORD="$ADMIN_PASSWORD" \
    -e POSTGRES_DB=nchat \
    "$IMAGE" >/dev/null
  local attempt
  for attempt in $(seq 1 60); do
    if docker exec "$CONTAINER" pg_isready -q -U "$ADMIN_USER" -d nchat 2>/dev/null; then
      ok "postgres is accepting connections ($IMAGE)"
      return 0
    fi
    sleep 1
  done
  fail "postgres did not become ready"
  return 1
}

# The same script the stateful layer runs as a Job, not a copy of it.
run_bootstrap() {
  docker cp "$BOOTSTRAP" "$CONTAINER:/tmp/postgres-bootstrap.sh" >/dev/null
  docker exec \
    -e POSTGRES_MIGRATOR_PASSWORD="$MIGRATOR_PASSWORD" \
    -e POSTGRES_APP_PASSWORD="$APP_PASSWORD" \
    -e PGUSER="$ADMIN_USER" -e PGDATABASE=nchat \
    "$CONTAINER" sh /tmp/postgres-bootstrap.sh >/dev/null
}

apply_migrations() {
  local schema file
  for schema in "${SCHEMAS[@]}"; do
    while IFS= read -r file; do
      as_migrator -f "$file" >/dev/null
    done < <(docker exec "$CONTAINER" sh -c "ls /tmp/migrations/$schema/*.up.sql 2>/dev/null | sort")
  done
}

# Everything nchat_migrator is supposed to own, and the count, so a restore that
# leaves objects behind is caught as well as one that misassigns them.
owned_tables() {
  scalar "SELECT count(*) FROM pg_tables
          WHERE schemaname = ANY(ARRAY['auth','chat','files'])
            AND tableowner = '$1'"
}

check_schema_owner() {
  local schema owner
  for schema in "${SCHEMAS[@]}"; do
    owner="$(scalar "SELECT pg_get_userbyid(nspowner) FROM pg_namespace WHERE nspname = '$schema'")"
    [[ "$owner" == nchat_migrator ]] ||
      fail "schema $schema is owned by '${owner:-nothing}', expected nchat_migrator ($2)"
  done
}

# nchat_app must be able to read and write, and must not be able to change
# shape. Both halves matter: a restore that grants too little breaks the
# application, and one that grants too much quietly ends least privilege.
check_runtime_privileges() {
  local stage="$1"
  as_app -tA -c "SELECT count(*) FROM auth.users" >/dev/null 2>&1 ||
    fail "nchat_app cannot read auth.users ($stage)"
  if as_app -tA -c "CREATE TABLE auth.privilege_probe (id int)" >/dev/null 2>&1; then
    fail "nchat_app was able to create a table; it must have no DDL ($stage)"
    as_migrator -c "DROP TABLE IF EXISTS auth.privilege_probe" >/dev/null 2>&1 || true
  fi
  if as_app -tA -c "DROP TABLE auth.users" >/dev/null 2>&1; then
    fail "nchat_app was able to drop a table ($stage)"
  fi
}

# The property a restore is for: the database can still be migrated afterwards.
# A column added and dropped again leaves the schema as it was found.
check_migrator_can_still_migrate() {
  local stage="$1"
  as_migrator -c "ALTER TABLE auth.users ADD COLUMN restore_probe integer" >/dev/null 2>&1 ||
    { fail "nchat_migrator cannot ALTER auth.users ($stage)"; return; }
  as_migrator -c "ALTER TABLE auth.users DROP COLUMN restore_probe" >/dev/null 2>&1 ||
    fail "nchat_migrator cannot DROP a column it just added ($stage)"
}

# ALTER DEFAULT PRIVILEGES only reaches objects created BY nchat_migrator, so a
# table made after the restore is the case that proves the grants are live
# rather than merely present on the restored tables.
check_default_privileges_reach_new_tables() {
  local stage="$1"
  as_migrator -c "CREATE TABLE auth.default_privilege_probe (id integer)" >/dev/null 2>&1 ||
    { fail "nchat_migrator cannot create a table ($stage)"; return; }
  as_app -c "INSERT INTO auth.default_privilege_probe VALUES (1)" >/dev/null 2>&1 ||
    fail "nchat_app cannot write to a table created after the restore; ALTER DEFAULT PRIVILEGES is not in effect ($stage)"
  as_migrator -c "DROP TABLE auth.default_privilege_probe" >/dev/null 2>&1 || true
}

main() {
  echo "=== production backup and restore test ==="
  echo
  command -v docker >/dev/null || { echo "docker is required" >&2; exit 1; }
  start_postgres || exit 1
  echo

  echo "--- fresh database ---"
  run_bootstrap
  docker cp "$MIGRATIONS" "$CONTAINER:/tmp/migrations" >/dev/null
  docker cp "$GRANTS" "$CONTAINER:/tmp/grant-runtime.sql" >/dev/null
  apply_migrations
  as_migrator -f /tmp/grant-runtime.sql >/dev/null
  BASELINE="$(owned_tables nchat_migrator)"
  [[ "$BASELINE" -gt 0 ]] || fail "migrations created no tables owned by nchat_migrator"
  ok "migrations applied as nchat_migrator; it owns $BASELINE tables"
  # pgcrypto and citext are trusted extensions, which is why a non-superuser
  # migrator can install them -- and therefore why it can restore them too.
  [[ "$(scalar "SELECT count(*) FROM pg_extension WHERE extname IN ('pgcrypto','citext')")" -eq 2 ]] ||
    fail "the trusted extensions the migrations install are missing"
  check_schema_owner nchat_migrator "fresh"
  check_runtime_privileges "fresh"
  ok "nchat_app reads and writes; it cannot create or drop"
  echo

  echo "--- backup ---"
  # As the admin, which can read every object regardless of owner, and named
  # explicitly: inside the container psql would otherwise default to the Unix
  # user, which is not this role.
  docker exec -e PGPASSWORD="$ADMIN_PASSWORD" "$CONTAINER" \
    pg_dump -U "$ADMIN_USER" -h 127.0.0.1 -d nchat \
    --format=custom --no-owner --no-privileges -f /tmp/nchat.dump >/dev/null ||
    { fail "pg_dump failed"; return; }
  [[ "$(docker exec "$CONTAINER" sh -c 'test -s /tmp/nchat.dump && echo yes')" == yes ]] ||
    fail "the dump file is empty"
  ok "custom-format dump created, without owners and without privileges"
  echo

  echo "--- restore into an empty database ---"
  docker exec -e PGPASSWORD="$ADMIN_PASSWORD" "$CONTAINER" \
    psql -U "$ADMIN_USER" -h 127.0.0.1 -d postgres -q \
    -c "DROP DATABASE nchat" -c "CREATE DATABASE nchat" >/dev/null
  run_bootstrap
  # The whole point: restored objects belong to whoever runs pg_restore, so it
  # is run as nchat_migrator. Running it as the admin is the bug this test
  # exists for -- see check_restoring_as_admin_is_the_broken_path below.
  docker exec -e PGPASSWORD="$MIGRATOR_PASSWORD" "$CONTAINER" \
    pg_restore -U nchat_migrator -h 127.0.0.1 -d nchat \
    --no-owner --exit-on-error /tmp/nchat.dump >/dev/null ||
    { fail "pg_restore as nchat_migrator failed"; return; }
  as_migrator -f /tmp/grant-runtime.sql >/dev/null ||
    fail "grant-runtime.sql could not be re-applied after the restore"
  ok "restored as nchat_migrator and re-applied the runtime grants"
  echo

  echo "--- after restore ---"
  RESTORED="$(owned_tables nchat_migrator)"
  [[ "$RESTORED" -eq "$BASELINE" ]] ||
    fail "nchat_migrator owns $RESTORED tables after the restore, expected $BASELINE"
  [[ "$(owned_tables "$ADMIN_USER")" -eq 0 ]] ||
    fail "tables in the application schemas are owned by the admin role after the restore"
  check_schema_owner nchat_migrator "after restore"
  ok "every schema and all $RESTORED tables are owned by nchat_migrator"
  check_migrator_can_still_migrate "after restore"
  ok "nchat_migrator can still apply DDL"
  check_runtime_privileges "after restore"
  ok "nchat_app still reads and writes, and still cannot create or drop"
  check_default_privileges_reach_new_tables "after restore"
  ok "ALTER DEFAULT PRIVILEGES still reaches tables created after the restore"
  echo

  check_restoring_as_admin_is_the_broken_path
  echo

  [[ "$ERRORS" -eq 0 ]] || {
    echo "backup and restore test failed with $ERRORS error(s)." >&2
    exit 1
  }
  echo "production backup and restore test passed."
}

# The negative case, stated as a test so the procedure cannot be "simplified"
# back into the failure it was written to avoid. Restoring as the admin must
# leave a database nchat_migrator cannot migrate.
check_restoring_as_admin_is_the_broken_path() {
  echo "--- restoring as the admin role is refused by reality ---"
  docker exec -e PGPASSWORD="$ADMIN_PASSWORD" "$CONTAINER" \
    psql -U "$ADMIN_USER" -h 127.0.0.1 -d postgres -q \
    -c "DROP DATABASE nchat" -c "CREATE DATABASE nchat" >/dev/null
  run_bootstrap
  docker exec -e PGPASSWORD="$ADMIN_PASSWORD" "$CONTAINER" \
    pg_restore -U "$ADMIN_USER" -h 127.0.0.1 -d nchat --no-owner /tmp/nchat.dump >/dev/null 2>&1 || true
  if as_migrator -c "ALTER TABLE auth.users ADD COLUMN should_not_work integer" >/dev/null 2>&1; then
    fail "restoring as the admin left a database nchat_migrator can still alter; this test no longer proves anything"
    as_migrator -c "ALTER TABLE auth.users DROP COLUMN should_not_work" >/dev/null 2>&1 || true
  else
    ok "an admin-owned restore leaves nchat_migrator unable to migrate, as documented"
  fi
}

main "$@"
