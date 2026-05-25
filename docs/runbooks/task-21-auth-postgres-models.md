# Runbook: Task-21 — Auth PostgreSQL Identity Models

## Overview

Migration `migrations/auth/000001_auth_identity_schema` establishes the foundational
identity data model for the auth-service. It is applied against the `nchat_auth`
PostgreSQL database and can be rolled back cleanly.

## Migration files

| File                                                   | Purpose                                            |
| ------------------------------------------------------ | -------------------------------------------------- |
| `migrations/auth/000001_auth_identity_schema.up.sql`   | Creates all auth tables, indexes, and extensions   |
| `migrations/auth/000001_auth_identity_schema.down.sql` | Drops all objects in safe reverse-dependency order |

## Applying the migration (local dev)

Prerequisites: PostgreSQL with `pgcrypto` and `citext` extension support
(standard in PostgreSQL 13+).

```bash
# Using golang-migrate (recommended):
migrate -path migrations/auth -database "postgres://user:pass@localhost/nchat_auth?sslmode=disable" up

# Or directly with psql:
psql "$DATABASE_URL" -f migrations/auth/000001_auth_identity_schema.up.sql
```

## Rolling back

```bash
migrate -path migrations/auth -database "$DATABASE_URL" down 1

# Or with psql:
psql "$DATABASE_URL" -f migrations/auth/000001_auth_identity_schema.down.sql
```

## CI validation (no database required)

```bash
pnpm migrations:check
# or
make migrations-check
```

The script checks:

- All `.up.sql` files have a matching `.down.sql`
- Files are non-empty
- No plaintext password/token column names
- Down migrations contain `DROP` statements
- Naming convention: `NNNNNN_name.(up|down).sql`

## Tables created

| Table                       | Purpose                                                   |
| --------------------------- | --------------------------------------------------------- |
| `users`                     | Core user identity, soft delete, auth source              |
| `user_password_credentials` | Hashed passwords only; expiry and forced-reset flags      |
| `user_invites`              | Admin-created email invitations; hashed tokens            |
| `user_sessions`             | Active sessions; refresh token hash; idle/absolute expiry |
| `user_devices`              | Per-user device registry; fingerprint hash; trust/revoke  |
| `login_attempts`            | Audit log for brute-force detection (RF-49, RF-50)        |
| `password_reset_tokens`     | Hashed one-time reset tokens (RF-48)                      |
| `auth_policy_settings`      | Single-row configurable policy (RF-47)                    |

## Security notes

- **Token fields**: all `token_hash` and `password_hash` columns store hashed values
  only. Raw tokens are never written to the database.
- **Email normalisation**: `citext` extension handles case-insensitive email
  uniqueness without manual `.lower()` normalisation.
- **IP addresses**: `inet` type is native PostgreSQL; supports IPv4 and IPv6
  without string manipulation.
- **Extensions**: `pgcrypto` provides `gen_random_uuid()` for UUID primary keys.

## PII handling

- `email` is PII — stored in `users.email` and `user_invites.email`.
- `full_name`, `avatar_url`, `display_name` are PII — stored in `users`.
- `ip_address` / `last_ip` are PII — stored in `user_sessions`, `user_devices`,
  `login_attempts`.
- `user_agent` is PII-adjacent — stored in `user_sessions` and `login_attempts`.

Soft-delete path (RF-55): set `users.deleted_at`. The `anonymized_at` column
records when PII fields were overwritten for LGPD compliance. No column names
change; the application layer performs the anonymisation write.

## Soft delete vs hard delete

- **Soft delete (RF-55, MVP)**: `users.deleted_at` + `users.status = 'deleted'`.
  Rows remain in the database; queries filter by `deleted_at IS NULL`.
- **Hard delete (RF-56, V1.0 only)**: `CASCADE` foreign keys on `user_id` are
  already wired. When hard-delete is implemented in V1.0, a single
  `DELETE FROM users WHERE id = $1` will propagate correctly. No schema change
  is needed at that point.

## Policy settings

`auth_policy_settings` enforces a single row via `CHECK (id = 1)`. The default
row is inserted by the migration. Updates should use `UPDATE auth_policy_settings
SET ... WHERE id = 1`.

## Related requirements

RF-44, RF-45, RF-46, RF-47, RF-48, RF-49, RF-50, RF-51, RF-52, RF-53, RF-54,
RF-55, RF-56.
