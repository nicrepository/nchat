# Runbook: Auth Session Expiry, Password Recovery, and Invites

## Overview

This runbook covers RF-46, RF-48, and RF-51 for auth-service.

| RF    | Requirement                                   | Implementation                                                                  |
| ----- | --------------------------------------------- | ------------------------------------------------------------------------------- |
| RF-46 | Convite por e-mail com link de ativação       | `POST /admin/invites` and `POST /auth/invites/accept` with expirable tokens     |
| RF-48 | Recuperação de senha via e-mail com token TTL | `POST /auth/password/forgot` and `POST /auth/password/reset`                    |
| RF-51 | Sessão expira após inatividade, padrão 1h     | Refresh rejects revoked/idle-expired/absolute-expired sessions and extends idle |

The PR prepares secure domain flows and a metadata-only e-mail handoff. It does
not implement frontend screens, SMTP, a notification worker, OAuth/OIDC, final
RBAC, or automatic login after invite/password reset.

---

## Session Expiry

`POST /auth/refresh` rejects refresh attempts when any of these are true:

- `auth.user_sessions.revoked_at IS NOT NULL`
- `auth.user_sessions.idle_expires_at <= now()`
- `auth.user_sessions.absolute_expires_at <= now()` when absolute expiry is set
- the owning user is not active or is soft-deleted

On successful refresh, auth-service rotates the refresh token and extends only
`idle_expires_at` using `auth.auth_policy_settings.session_idle_timeout_minutes`
(default `60`). It does not extend `absolute_expires_at`.

`POST /auth/logout` remains idempotent: unknown, expired, or already-revoked
refresh tokens still return `204 No Content` through the HTTP contract.

---

## Password Recovery

### `POST /auth/password/forgot`

Public endpoint, 4 KiB body cap, in-memory rate limit.

Request:

```json
{
  "email": "user@example.com"
}
```

Response:

- `202 Accepted` with an empty body for known, unknown, invalid, deleted,
  suspended, or locked users.
- The response never reveals whether the e-mail exists.

Behavior:

1. Normalize e-mail.
2. If an active manual non-deleted user exists, generate an opaque token of at
   least 32 random bytes.
3. Store only a domain-separated HMAC-SHA-256 hash in
   `auth.password_reset_tokens.token_hash`.
4. Supersede previous unused reset tokens for the user by setting `used_at`.
5. Insert a metadata-only `auth.email_outbox` row with `kind='password_reset'`,
   `reset_token_id`, `user_id`, recipient metadata, status, attempts, and
   timestamps.
6. Do not return the token over HTTP.

### `POST /auth/password/reset`

Public endpoint, 4 KiB body cap, in-memory rate limit.

Request:

```json
{
  "token": "opaque-token-from-email",
  "new_password": "NewStrongPassword@123"
}
```

Response:

- `204 No Content` on success.
- `401 invalid_token` for unknown, expired, used, or otherwise invalid tokens.
- `400 bad_request` for weak passwords or malformed input.

Behavior:

1. Hash the submitted token with the password-reset HMAC domain prefix.
2. Validate the new password using `auth.auth_policy_settings`.
3. Hash the new password with the existing Argon2id PHC implementation.
4. In one transaction, lock the reset token row, reject expired/used tokens,
   update the password credential, mark the token used, revoke all sessions for
   the user, and revoke active refresh-token history rows.
5. Do not auto-login.

---

## Admin Invites

### `POST /admin/invites`

Admin endpoint guarded by `AdminBootstrapGuard` and `X-NChat-Admin-Token` until
final RBAC exists.

- Empty `ADMIN_BOOTSTRAP_TOKEN` disables the endpoint with `503`.
- Wrong or missing `X-NChat-Admin-Token` returns `401`.
- The admin token is never logged.

Request:

```json
{
  "email": "user@example.com",
  "display_name": "User Name",
  "full_name": "User Full Name"
}
```

Response:

```json
{
  "id": "invite-id",
  "email": "user@example.com",
  "created_at": "2026-05-28T12:00:00Z"
}
```

Behavior:

1. Normalize and validate e-mail and `display_name`.
2. If any user already exists with that e-mail, return `409`.
3. If an active pending invite exists, return `409`. This PR does not rotate
   pending invite tokens.
4. Generate an opaque token of at least 32 random bytes.
5. Store only a domain-separated HMAC-SHA-256 hash in
   `auth.user_invites.token_hash`.
6. Insert a metadata-only `auth.email_outbox` row with `kind='invite'`,
   `invite_id`, recipient metadata, status, attempts, and timestamps.
7. Do not return the token over HTTP.

### `POST /auth/invites/accept`

Public endpoint, 4 KiB body cap, in-memory rate limit.

Request:

```json
{
  "token": "opaque-token-from-email",
  "display_name": "User Name",
  "full_name": "User Full Name",
  "password": "StrongPassword@123"
}
```

Response:

```json
{
  "id": "user-id",
  "email": "user@example.com",
  "display_name": "User Name",
  "full_name": "User Full Name",
  "created_at": "2026-05-28T12:00:00Z"
}
```

Behavior:

1. Hash the submitted token with the invite HMAC domain prefix.
2. Validate the password policy and hash the password with Argon2id PHC.
3. In one transaction, lock the invite row, reject expired/accepted/revoked
   tokens, create the user, create the password credential, and mark the invite
   accepted.
4. Do not create an initial session. The user logs in separately.

---

## Metadata-Only Email Outbox

`auth.email_outbox` is a handoff table only. It stores:

- `id`
- `kind` (`password_reset` or `invite`)
- `to_email`
- `subject`
- `template_key`
- `reset_token_id` or `invite_id`
- `user_id` when applicable
- non-sensitive `payload` JSONB, currently `{}`
- `status`, `attempts`, `created_at`, `sent_at`, and `last_error`

It must not store:

- raw reset or invite tokens
- full links containing tokens
- token hashes in payload
- passwords or password hashes
- access tokens or refresh tokens
- e-mail bodies containing token-bearing links

### Known delivery limitation

This PR does not deliver reset or invite e-mails end-to-end because the raw token
is intentionally not persisted in `auth.email_outbox`. A future e-mail or
notification-service task must choose a safe strategy, such as generating the
link inside the same trusted delivery boundary, encrypting a payload with
explicit key management, or handing the token directly to a notification worker
without plaintext database persistence.

---

## Security Notes

- Reset/invite tokens are opaque random values with at least 32 bytes of entropy.
- Only HMAC-SHA-256 token hashes are persisted in token tables.
- Password reset and invite hashes use separate HMAC domain prefixes.
- Public endpoints have 4 KiB request body caps and in-memory rate limiting.
- Token validation errors are generic to avoid token oracle behavior.
- Forgot-password responses are generic to avoid account enumeration.
- Responses do not include raw tokens, token hashes, password hashes, access
  tokens, or refresh tokens.
- Sensitive request fields and e-mail bodies are not logged by these handlers.

---

## Out of Scope

- Frontend screens
- Real SMTP or transactional e-mail provider
- Notification-service worker
- OAuth/OIDC
- Final RBAC
- Auto-login after password reset or invite acceptance
- Distributed rate limiting

---

## Validation

Required validation for this PR:

```bash
bash -n scripts/db/migrate.sh scripts/ci/migrations-check.sh
pnpm migrations:check
cd services/auth-service && go test -count=1 ./...
pnpm fmt:go
pnpm format:check:docs
pnpm lint:go
pnpm vet:go
pnpm test:coverage:go:check
pnpm run ci
make ci
semgrep scan --config p/owasp-top-ten --config p/secrets services/auth-service migrations/auth docs/runbooks/task-auth-session-recovery-invites.md
```
