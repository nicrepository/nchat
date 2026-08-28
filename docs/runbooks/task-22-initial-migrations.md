# Runbook: Task-22 — Initial Migration Framework

## Overview

TASK-22 establishes the migration runner, CI validation, and smoke-test tooling
that make database migrations easy to run, validate, and verify locally.
It builds on the auth identity schema introduced in TASK-21 without duplicating it.

## Convention

```
migrations/
  <domain>/
    <NNNNNN>_<name>.up.sql    # forward migration
    <NNNNNN>_<name>.down.sql  # rollback
```

- Six-digit zero-padded version (e.g. `000001`)
- Lowercase `snake_case` name
- Every `.up.sql` must have a matching `.down.sql`
- All `CREATE TABLE` statements must be schema-qualified (e.g. `auth.users`)
- Down migrations must not `DROP EXTENSION`

## Scripts added

| Script                           | Purpose                                           |
| -------------------------------- | ------------------------------------------------- |
| `scripts/db/migrate.sh`          | Migration runner (up, down, status, reset, smoke) |
| `scripts/db/migrations-smoke.sh` | DB smoke test (apply + verify + rollback)         |
| `scripts/ci/migrations-check.sh` | Static CI validation (no DB required)             |

## Makefile / npm targets

| Make                     | pnpm                     | Description                              |
| ------------------------ | ------------------------ | ---------------------------------------- |
| `make migrations-check`  | `pnpm migrations:check`  | CI static validation                     |
| `make migrations-up`     | `pnpm migrations:up`     | Apply pending migrations                 |
| `make migrations-down`   | `pnpm migrations:down`   | Roll back last migration                 |
| `make migrations-status` | `pnpm migrations:status` | Show applied / pending                   |
| `make migrations-reset`  | `pnpm migrations:reset`  | Roll back all (interactive; destructive) |
| `make migrations-smoke`  | `pnpm migrations:smoke`  | DB smoke test                            |

## Applying migrations (local dev)

Prerequisites: `make dev-env-up` (starts local PostgreSQL via Docker Compose)

```bash
# Apply all pending
make migrations-up

# Check status
make migrations-status

# Dry-run (see SQL without executing)
bash scripts/db/migrate.sh up --dry-run

# Roll back last migration
make migrations-down

# Roll back last 3
bash scripts/db/migrate.sh down --steps 3

# Destructive: roll back every applied migration interactively
make migrations-reset

# Destructive: roll back every applied migration from a non-interactive shell
bash scripts/db/migrate.sh reset --force
```

## CI validation (no DB required)

```bash
make migrations-check
# or
pnpm migrations:check
```

Checks performed:

1. Every `.up.sql` has a matching `.down.sql`, with no orphan `.down.sql` files
2. Domain and migration filenames match the safe naming convention
3. No migration file is empty
4. Every `.up.sql` and `.down.sql` contains `BEGIN;` and `COMMIT;`
5. No plaintext credential columns (rejects `token`, `password`, `api_key` etc. without `_hash`)
6. Down migrations contain at least one `DROP` statement
7. Down migrations reject broad destructive SQL such as `DROP DATABASE`, `DROP SCHEMA`, `DROP OWNED`, `TRUNCATE`, unqualified `DROP TABLE`, `DROP TABLE public.*`, and `DROP EXTENSION`
8. All `CREATE TABLE` statements are schema-qualified, including multiline statements
9. Auth domain migrations reference `auth.` schema

## Smoke test (requires Docker Compose)

The smoke test requires a running local PostgreSQL container and is not CI-blocking.

```bash
make dev-env-up        # start PostgreSQL
make migrations-smoke  # apply → verify → rollback → verify clean
make dev-env-down      # stop when done
```

## schema_migrations table

The runner tracks state in `public.schema_migrations` (auto-created):

```sql
CREATE TABLE public.schema_migrations (
  id              SERIAL PRIMARY KEY,
  domain          TEXT        NOT NULL,
  filename        TEXT        NOT NULL,
  checksum_sha256 TEXT        NOT NULL,
  dirty           BOOLEAN     NOT NULL DEFAULT false,
  in_progress     BOOLEAN     NOT NULL DEFAULT false,
  applied_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT schema_migrations_uq UNIQUE (domain, filename)
);
```

## Environment

Migration runner reads `POSTGRES_*` from `infra/compose/.env.dev`
(falls back to `.env.dev.example`). `PGPASSWORD` is passed via environment
variable and never printed or put on the command line. Non-interactive reset
requires `--force` so automation cannot roll back all migrations by accident.
The runner takes a PostgreSQL advisory lock during `up`, `down`, and `reset`,
records SHA-256 checksums, and leaves dirty/in-progress rows behind if a run is
interrupted so later runs stop for manual repair.

## Adding a new migration

```bash
# Determine next version number
ls migrations/<domain>/ | sort | tail -1

# Create files manually
touch migrations/<domain>/000002_add_foo.up.sql
touch migrations/<domain>/000002_add_foo.down.sql
```

Then write the SQL, keep `BEGIN;` and `COMMIT;` in both files, add `SET LOCAL search_path = <domain>, public;` inside the transaction when schema lookup is needed, and run `make migrations-check` to validate.

## Related

- Architecture: [docs/architecture/database-migrations.md](../architecture/database-migrations.md)
- Auth schema: [docs/architecture/auth-data-model.md](../architecture/auth-data-model.md)
- TASK-21 runbook: [docs/runbooks/task-21-auth-postgres-models.md](task-21-auth-postgres-models.md)
