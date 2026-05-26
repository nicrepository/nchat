# Database Migrations

## Overview

NChat uses plain SQL migrations tracked by a lightweight `psql`-based runner.
No external migration framework is required — only `psql` and Bash.

## Layout

```
migrations/
  auth/
    000001_auth_identity_schema.up.sql
    000001_auth_identity_schema.down.sql
    .gitkeep
  chat/
    .gitkeep
  files/
    .gitkeep
  notifications/
    .gitkeep
```

Each domain directory holds migrations for one service or bounded context.
The `.gitkeep` files reserve slots for future domains.

## File naming

```
NNNNNN_<name>.(up|down).sql
```

- `NNNNNN` — six-digit zero-padded sequence starting at `000001`
- `<name>` — lowercase `snake_case` description
- One pair per logical change; never combine unrelated schema changes

Example: `000001_auth_identity_schema.up.sql`

## Ordering

Migrations are applied in lexicographic order within each domain.
The runner applies all domains in alphabetical order. Within a release,
cross-domain dependencies must be avoided; use separate PRs if needed.

## Conventions

| Rule                                                                    | Reason                                                      |
| ----------------------------------------------------------------------- | ----------------------------------------------------------- |
| Every `.up.sql` must have a `.down.sql`                                 | Rollback must always be possible                            |
| All `CREATE TABLE` must be schema-qualified (`auth.users`, not `users`) | Prevents public-schema pollution                            |
| Down migrations must not `DROP EXTENSION`                               | Extensions are database-wide; dropping breaks all schemas   |
| No plaintext credential columns                                         | Security: store only hashes (`password_hash`, `token_hash`) |
| `BEGIN` / `COMMIT` wrap each migration                                  | DDL is transactional in PostgreSQL 16                       |

## Tracking table

`public.schema_migrations` is auto-created by the runner on first use:

```sql
CREATE TABLE public.schema_migrations (
  id         SERIAL PRIMARY KEY,
  domain     TEXT        NOT NULL,
  filename   TEXT        NOT NULL,    -- e.g. "000001_auth_identity_schema"
  applied_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT schema_migrations_uq UNIQUE (domain, filename)
);
```

The table lives in `public` so it is accessible regardless of which schemas
have been migrated. It is never included in application auth logic.

## Schema map

| Domain          | PostgreSQL schema | Service                 |
| --------------- | ----------------- | ----------------------- |
| `auth`          | `auth`            | auth-service            |
| `chat`          | `chat`            | chat-service (planned)  |
| `files`         | `files`           | media-service (planned) |
| `notifications` | `notifications`   | notif-service (planned) |

## Tooling

| Tool       | File                             | Purpose                    |
| ---------- | -------------------------------- | -------------------------- |
| Runner     | `scripts/db/migrate.sh`          | up / down / status / reset |
| Smoke test | `scripts/db/migrations-smoke.sh` | end-to-end DB verification |
| CI check   | `scripts/ci/migrations-check.sh` | static validation (no DB)  |

See [docs/runbooks/task-22-initial-migrations.md](../runbooks/task-22-initial-migrations.md)
for operational details.

## PostgreSQL version

Target: **PostgreSQL 16**. Migrations use:

- `gen_random_uuid()` (pgcrypto)
- `CITEXT` (citext)
- `INET` type
- `TIMESTAMPTZ`
- Transactional DDL (`BEGIN` / `COMMIT`)

## Adding migrations

1. Determine the next version number for the domain.
2. Create `migrations/<domain>/NNNNNN_<name>.up.sql` and `.down.sql`.
3. Wrap SQL in `BEGIN; ... COMMIT;`.
4. Run `pnpm migrations:check` to validate offline.
5. Run `pnpm migrations:up` against a local DB to verify execution.
6. Open a PR; CI runs `migrations:check` automatically.
