# Runbook: Task-23 — Admin Manual User Creation

## Overview

Implements RF-45: cadastro manual pelo admin.
Adds `POST /admin/users` to `auth-service` so an admin can create a user
with an initial Argon2id-hashed password, validated against the password
policy stored in `auth.auth_policy_settings`.

## Out of scope (not implemented in this task)

- Login / session issuance / JWT
- OAuth/OIDC integration
- Password reset email
- Invite flow
- Frontend admin UI
- Full RBAC (see temporary guard below)

## Endpoint

```
POST /admin/users
```

Headers:

- `Content-Type: application/json`
- `X-NChat-Admin-Token: <ADMIN_BOOTSTRAP_TOKEN>` — **temporary bootstrap guard**

Request body:

```json
{
  "email": "user@example.com",
  "display_name": "User Name",
  "full_name": "Full Name",
  "initial_password": "TemporaryP@ss1",
  "must_change_password": true
}
```

Response (201):

```json
{
  "data": {
    "id": "...",
    "email": "user@example.com",
    "display_name": "User Name",
    "status": "active",
    "auth_source": "manual",
    "email_verified_at": "2026-05-26T10:00:00Z",
    "created_at": "2026-05-26T10:00:00Z"
  }
}
```

Error responses:

- `400` — invalid input or password policy violation
- `401` — missing or wrong `X-NChat-Admin-Token`
- `409` — duplicate email
- `500` — internal error
- `503` — `ADMIN_BOOTSTRAP_TOKEN` not set, or database not configured

## Environment variables

| Variable                     | Required       | Description                                                  |
| ---------------------------- | -------------- | ------------------------------------------------------------ |
| `DATABASE_URL`               | Yes            | pgx DSN — `postgres://user:pass@host/dbname`                 |
| `ADMIN_BOOTSTRAP_TOKEN`      | Yes            | Temporary admin guard token. If empty, endpoint returns 503. |
| `DB_CONNECT_TIMEOUT_SECONDS` | No (default 5) | Timeout for initial DB connection                            |

## Temporary admin guard

`X-NChat-Admin-Token` is a **dev/bootstrap-only** mechanism. It is NOT final RBAC.
Replace with proper auth middleware before production.
The token is never logged by the service.

## Running locally

```bash
# Start PostgreSQL
make dev-env-up

# Apply migrations
make migrations-up

# Run auth-service with required env
export DATABASE_URL="postgres://nchat:nchat@localhost:5432/nchat?sslmode=disable"
export ADMIN_BOOTSTRAP_TOKEN="replace-with-secure-token"
cd services/auth-service && go run ./cmd/auth-service/

# Create a user (separate terminal)
curl -s -X POST http://localhost:8081/admin/users \
  -H "Content-Type: application/json" \
  -H "X-NChat-Admin-Token: replace-with-secure-token" \
  -d '{
    "email": "admin@example.com",
    "display_name": "Admin User",
    "initial_password": "ChangeMe@123",
    "must_change_password": true
  }' | jq .
```

## Password policy

Applied from `auth.auth_policy_settings` (seeded with defaults):

- Minimum length: 12 characters
- Requires: uppercase, lowercase, number, symbol

## Password storage

Argon2id PHC format: `$argon2id$v=19$m=65536,t=3,p=4$<salt>$<hash>`
Never stored as plaintext.

## Related

- Auth schema: [docs/architecture/auth-data-model.md](../architecture/auth-data-model.md)
- Migration framework: [docs/runbooks/task-22-initial-migrations.md](task-22-initial-migrations.md)
