#!/usr/bin/env bash
set -Eeuo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"
POSTGRES_IMAGE="postgres:16.10-alpine@sha256:029660641a0cfc575b14f336ba448fb8a75fd595d42e1fa316b9fb4378742297"
CONTAINER="nchat-db-smoke-$$"
MIGRATIONS_IMAGE="nchat-migrations-smoke:$$"
BOOTSTRAP="$ROOT_DIR/infra/k8s/overlays/nchat-dev-server/data/postgres-bootstrap.sh"
MIGRATOR_PASSWORD="$(printf 'nchat-migrator-smoke-%s' "$$" | sha256sum | cut -c1-32)"
APP_PASSWORD="$(printf 'nchat-app-smoke-%s' "$$" | sha256sum | cut -c1-32)"

cleanup() {
  docker rm -f "$CONTAINER" >/dev/null 2>&1 || true
  docker image rm -f "$MIGRATIONS_IMAGE" >/dev/null 2>&1 || true
}
trap cleanup EXIT

docker run --detach --name "$CONTAINER" \
  --env POSTGRES_DB=nchat \
  --env POSTGRES_USER=nchat_admin \
  --env POSTGRES_HOST_AUTH_METHOD=trust \
  --volume "$BOOTSTRAP:/bootstrap.sh:ro" \
  "$POSTGRES_IMAGE" >/dev/null

database_ready=false

for _ in {1..60}; do
  if docker exec "$CONTAINER" \
    psql \
      --username nchat_admin \
      --dbname nchat \
      --tuples-only \
      --no-align \
      --command 'SELECT 1;' 2>/dev/null |
    grep -Fxq '1'; then
    database_ready=true
    break
  fi

  sleep 1
done

if [[ "$database_ready" != true ]]; then
  echo "PostgreSQL did not finish initializing the nchat database." >&2
  docker logs "$CONTAINER" >&2 || true
  exit 1
fi

for _ in 1 2; do
  docker exec \
    --env PGUSER=nchat_admin \
    --env PGDATABASE=nchat \
    --env POSTGRES_MIGRATOR_PASSWORD="$MIGRATOR_PASSWORD" \
    --env POSTGRES_APP_PASSWORD="$APP_PASSWORD" \
    "$CONTAINER" /bin/sh /bootstrap.sh >/dev/null
done

docker build --file "$ROOT_DIR/Dockerfile.migrations" --tag "$MIGRATIONS_IMAGE" "$ROOT_DIR" >/dev/null
docker run --rm --network "container:$CONTAINER" \
  --env MIGRATIONS_DATABASE_URL="postgresql://nchat_migrator:$MIGRATOR_PASSWORD@127.0.0.1:5432/nchat" \
  "$MIGRATIONS_IMAGE" up >/dev/null

role_flags="$(docker exec "$CONTAINER" psql --username nchat_admin --dbname nchat --tuples-only --no-align \
  --command "SELECT rolname || ':' || rolsuper || ':' || rolcreatedb || ':' || rolcreaterole FROM pg_roles WHERE rolname IN ('nchat_migrator','nchat_app') ORDER BY rolname;")"
grep -Fxq 'nchat_app:false:false:false' <<<"$role_flags"
grep -Fxq 'nchat_migrator:false:false:false' <<<"$role_flags"

docker exec "$CONTAINER" psql --username nchat_migrator --dbname nchat --set=ON_ERROR_STOP=1 \
  --command 'CREATE TABLE auth.runtime_privilege_smoke (id bigserial PRIMARY KEY, value text NOT NULL);' >/dev/null
docker exec "$CONTAINER" psql --username nchat_app --dbname nchat --set=ON_ERROR_STOP=1 \
  --command "INSERT INTO auth.runtime_privilege_smoke(value) VALUES ('ok'); UPDATE auth.runtime_privilege_smoke SET value='updated'; SELECT value FROM auth.runtime_privilege_smoke; DELETE FROM auth.runtime_privilege_smoke;" >/dev/null

if docker exec "$CONTAINER" psql --username nchat_app --dbname nchat --set=ON_ERROR_STOP=1 \
  --command 'CREATE TABLE auth.runtime_must_not_create (id integer);' >/dev/null 2>&1; then
  echo "runtime role unexpectedly created a table" >&2
  exit 1
fi

docker exec "$CONTAINER" psql --username nchat_migrator --dbname nchat \
  --command 'DROP TABLE auth.runtime_privilege_smoke;' >/dev/null

echo "nchat-dev database smoke test passed."
