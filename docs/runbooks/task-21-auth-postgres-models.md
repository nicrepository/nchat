# Runbook: Task-21 — Auth PostgreSQL Identity Models

## Overview

Migration `migrations/auth/000001_auth_identity_schema` establishes the foundational
identity data model for the auth-service. It is applied to the `auth` schema in
the existing `nchat` PostgreSQL database and can be rolled back cleanly.

## Migration files

| File                                                   | Purpose                                            |
| ------------------------------------------------------ | -------------------------------------------------- |
| `migrations/auth/000001_auth_identity_schema.up.sql`   | Creates the auth schema tables and indexes         |
| `migrations/auth/000001_auth_identity_schema.down.sql` | Drops auth tables in safe reverse-dependency order |

## Applying the migration (local dev)

Prerequisites: PostgreSQL with `pgcrypto` and `citext` extension support
(standard in PostgreSQL 13+).

```bash
# Using golang-migrate (recommended):
migrate -path migrations/auth -database "postgres://user:pass@localhost/nchat?sslmode=disable" up

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
- No plaintext credential column names such as `password_raw`, `raw_token`, `api_key`, `secret`, or unhashed `token`
- Down migrations contain `DROP` statements
- Naming convention: `NNNNNN_name.(up|down).sql`

## Tables created

| Table                            | Purpose                                                   |
| -------------------------------- | --------------------------------------------------------- |
| `auth.users`                     | Core user identity, soft delete, auth source              |
| `auth.user_password_credentials` | Hashed passwords only; expiry and forced-reset flags      |
| `auth.user_invites`              | Admin-created email invitations; hashed tokens            |
| `auth.user_sessions`             | Active sessions; refresh token hash; idle/absolute expiry |
| `auth.user_devices`              | Per-user device registry; fingerprint hash; trust/revoke  |
| `auth.login_attempts`            | Audit log for brute-force detection (RF-49, RF-50)        |
| `auth.password_reset_tokens`     | Hashed one-time reset tokens (RF-48)                      |
| `auth.auth_policy_settings`      | Single-row configurable policy (RF-47)                    |

## Security notes

- **Token fields**: all `token_hash` and `password_hash` columns store hashed values
  only. Raw tokens are never written to the database.
- **Email normalisation**: `citext` extension handles case-insensitive email
  uniqueness without manual `.lower()` normalisation.
- **IP addresses**: `inet` type is native PostgreSQL; supports IPv4 and IPv6
  without string manipulation.
- **Extensions**: `pgcrypto` provides `gen_random_uuid()` for UUID primary keys.
  Rollback intentionally keeps database-wide extensions because they may be shared.

## PII handling

- `email` is PII — stored in `auth.users.email` and `auth.user_invites.email`.
- `full_name`, `avatar_url`, `display_name` are PII — stored in `auth.users`.
- `ip_address` / `last_ip` are PII — stored in `auth.user_sessions`,
  `auth.user_devices`, `auth.login_attempts`.
- `user_agent` is PII-adjacent — stored in `auth.user_sessions` and
  `auth.login_attempts`.

Soft-delete path (RF-55): set `auth.users.deleted_at`. The `anonymized_at` column
records when PII fields were overwritten for LGPD compliance. No column names
change; the application layer performs the anonymisation write.

## Soft delete vs hard delete

- **Soft delete (RF-55, MVP)**: `auth.users.deleted_at` + `auth.users.status = 'deleted'`.
  Rows remain in the database; queries filter by `deleted_at IS NULL`.
- **Hard delete (RF-56, V1.0 only)**: `CASCADE` foreign keys on `user_id` are
  already wired. When hard-delete is implemented in V1.0, a single
  `DELETE FROM auth.users WHERE id = $1` will propagate correctly. No schema change
  is needed at that point.

## Policy settings

`auth.auth_policy_settings` enforces a single row via `CHECK (id = 1)`. The default
row is inserted by the migration. Numeric policy values are constrained to positive
values. Updates should use `UPDATE auth.auth_policy_settings SET ... WHERE id = 1`.

## Related requirements

RF-44, RF-45, RF-46, RF-47, RF-48, RF-49, RF-50, RF-51, RF-52, RF-53, RF-54,
RF-55, RF-56.
