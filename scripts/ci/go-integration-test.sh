#!/usr/bin/env bash
# Owner of `Tests / Go Integration`: the Go suites that need a real PostgreSQL
# and can be run as a whole package without colliding with each other.
#
# Pure unit tests must never wait for a database, so these live here rather than
# in `go-test.sh`, which exports no DSN and therefore skips every opt-in suite.
#
# Each suite gets a database of its own. That is not caution, it is a hard
# requirement: every one of them resets the schema it owns on setup, so two
# suites sharing a database make the result depend on execution order. Every
# database must be empty and its name must end in `_test` -- the suites refuse
# anything else -- and none of them needs `scripts/db/migrate.sh` first, because
# each applies the migration files it needs itself.
#
# Deliberately NOT here, each for a reason proved by running it:
#
#   * The Link Safety, notification outbox, push and reminder suites are owned by
#     `Tests / Go Coverage`, which must execute them to merge their profile into
#     the 90% threshold. Running them here too would be the duplication issue
#     #931 exists to remove.
#   * The remaining chat-service and file-service PostgreSQL suites have no owner
#     yet. They are not runnable as a package today: chat-service's collide on
#     shared fixtures even in isolation, file-service's `internal/storage` hangs
#     on a session advisory lock, and its `internal/service` suites additionally
#     need SeaweedFS. See docs/testing/taxonomy.md; fixing them is its own issue.
#
# Local run (Docker):
#   docker run -d --name nchat-int-pg -e POSTGRES_USER=nchat \
#     -e POSTGRES_PASSWORD=REPLACE_ME -e POSTGRES_DB=nchat_test -p 55432:5432 postgres:16
#   for db in admin_test auth_test media_test; do
#     docker exec nchat-int-pg createdb -U nchat "$db"
#   done
#   base="postgresql://nchat:REPLACE_ME@localhost:55432"
#   CHAT_TEST_DATABASE_URL="$base/nchat_test?sslmode=disable" \
#   ADMIN_TEST_DATABASE_URL="$base/admin_test?sslmode=disable" \
#   AUTH_TEST_DATABASE_URL="$base/auth_test?sslmode=disable" \
#   MEDIA_TEST_DATABASE_URL="$base/media_test?sslmode=disable" \
#     make test-integration-go
#   docker rm -f nchat-int-pg
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"

# module | DSN variable | package | extra `go test` flags
#
# chat-service is the one entry restricted to a single family: the rest of its
# PostgreSQL suites belong to Go Coverage or have no owner yet (see above).
# media-service carries a `//go:build integration` tag, so without `-tags` its
# suite is not even compiled.
SUITES=(
  "services/chat-service|CHAT_TEST_DATABASE_URL|./internal/storage|-run ^TestChannelMembershipContractPostgreSQL_"
  "services/admin-service|ADMIN_TEST_DATABASE_URL|./internal/storage|"
  "services/auth-service|AUTH_TEST_DATABASE_URL|./internal/storage|"
  "services/media-service|MEDIA_TEST_DATABASE_URL|./internal/storage|-tags integration"
)

for suite in "${SUITES[@]}"; do
  IFS='|' read -r module dsn_variable package flags <<<"$suite"

  if [ -z "${!dsn_variable:-}" ]; then
    echo "$dsn_variable is required: $module has no database to run against." >&2
    exit 1
  fi

  read -r -a go_test_flags <<<"$flags"

  echo "==> go test integration $module $package"
  (cd "$ROOT/$module" && go test "${go_test_flags[@]}" -count=1 "$package")
done

echo "Go integration suites passed."
