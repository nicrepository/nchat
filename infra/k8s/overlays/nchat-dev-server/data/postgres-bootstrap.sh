#!/bin/sh
# POSIX sh: postgres:alpine does not include Bash. This script has no pipelines.
set -eu

until pg_isready --quiet; do
  sleep 2
done

psql --no-password --set=ON_ERROR_STOP=1 \
  --set=migrator_password="$POSTGRES_MIGRATOR_PASSWORD" \
  --set=app_password="$POSTGRES_APP_PASSWORD" <<'SQL'
SELECT format('CREATE ROLE nchat_migrator LOGIN PASSWORD %L', :'migrator_password')
WHERE NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'nchat_migrator') \gexec
SELECT format('CREATE ROLE nchat_app LOGIN PASSWORD %L', :'app_password')
WHERE NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'nchat_app') \gexec

ALTER ROLE nchat_migrator LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS PASSWORD :'migrator_password';
ALTER ROLE nchat_app LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS PASSWORD :'app_password';

REVOKE ALL ON DATABASE nchat FROM nchat_migrator;
REVOKE ALL ON DATABASE nchat FROM nchat_app;
REVOKE CREATE, TEMPORARY ON DATABASE nchat FROM PUBLIC;
GRANT CONNECT, CREATE ON DATABASE nchat TO nchat_migrator;
GRANT CONNECT ON DATABASE nchat TO nchat_app;

REVOKE CREATE ON SCHEMA public FROM PUBLIC;
GRANT USAGE, CREATE ON SCHEMA public TO nchat_migrator;
GRANT USAGE ON SCHEMA public TO nchat_app;

SELECT format('ALTER SCHEMA %I OWNER TO nchat_migrator', nspname)
FROM pg_namespace
WHERE nspname IN ('auth', 'chat') \gexec
SQL
