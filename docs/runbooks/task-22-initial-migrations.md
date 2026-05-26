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

| Make                     | pnpm                     | Description                 |
| ------------------------ | ------------------------ | --------------------------- |
| `make migrations-check`  | `pnpm migrations:check`  | CI static validation        |
| `make migrations-up`     | `pnpm migrations:up`     | Apply pending migrations    |
| `make migrations-down`   | `pnpm migrations:down`   | Roll back last migration    |
| `make migrations-status` | `pnpm migrations:status` | Show applied / pending      |
| `make migrations-reset`  | `pnpm migrations:reset`  | Roll back all (interactive) |
| `make migrations-smoke`  | `pnpm migrations:smoke`  | DB smoke test               |

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

# Roll back everything
make migrations-reset
```

## CI validation (no DB required)

```bash
make migrations-check
# or
pnpm migrations:check
```

Checks performed:

1. Every `.up.sql` has a matching `.down.sql`
2. No migration file is empty
3. No plaintext credential columns (rejects `token`, `password`, `api_key` etc. without `_hash`)
4. Down migrations contain at least one `DROP` statement
5. Naming convention: `NNNNNN_name.(up|down).sql`
6. Down migrations do not use `DROP EXTENSION`
7. All `CREATE TABLE` statements are schema-qualified
8. Auth domain migrations reference `auth.` schema

## Smoke test (requires Docker Compose)

```bash
make dev-env-up        # start PostgreSQL
make migrations-smoke  # apply → verify → rollback → verify clean
make dev-env-down      # stop when done
```

## schema_migrations table

The runner tracks state in `public.schema_migrations` (auto-created):

```sql
CREATE TABLE public.schema_migrations (
  id         SERIAL PRIMARY KEY,
  domain     TEXT        NOT NULL,
  filename   TEXT        NOT NULL,
  applied_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT schema_migrations_uq UNIQUE (domain, filename)
);
```

## Environment

Migration runner reads `POSTGRES_*` from `infra/compose/.env.dev`
(falls back to `.env.dev.example`). `PGPASSWORD` is passed via environment
variable and never printed or put on the command line.

## Adding a new migration

```bash
# Determine next version number
ls migrations/<domain>/ | sort | tail -1

# Create files manually
touch migrations/<domain>/000002_add_foo.up.sql
touch migrations/<domain>/000002_add_foo.down.sql
```

Then write the SQL and run `make migrations-check` to validate.

## Related

- Architecture: [docs/architecture/database-migrations.md](../architecture/database-migrations.md)
- Auth schema: [docs/architecture/auth-data-model.md](../architecture/auth-data-model.md)
- TASK-21 runbook: [docs/runbooks/task-21-auth-postgres-models.md](task-21-auth-postgres-models.md)
