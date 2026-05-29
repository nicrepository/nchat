# Runbook: Auth Session Expiry, Password Recovery, and Invites

## Overview

This runbook covers RF-46, RF-48, and RF-51 for auth-service.

| RF    | Requirement                                   | Implementation                                                                                                                 |
| ----- | --------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------ |
| RF-46 | Convite por e-mail com link de ativacao       | `POST /admin/invites` and `POST /auth/invites/accept` with expirable tokens and encrypted outbox handoff                       |
| RF-48 | Recuperacao de senha via e-mail com token TTL | `POST /auth/password/forgot` and `POST /auth/password/reset` with expirable tokens and encrypted outbox handoff                |
| RF-51 | Sessao expira apos inatividade, padrao 1h     | Refresh rejects revoked/idle-expired/absolute-expired sessions and extends `session_idle_timeout_minutes` (default 60 minutes) |

The PR prepares secure domain flows and an encrypted e-mail token handoff. It
does not implement frontend screens, SMTP, a notification worker, OAuth/OIDC,
final RBAC, or automatic login after invite/password reset.

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

Public endpoint, 4 KiB body cap, endpoint-specific IP rate limit, and secondary
normalized-email HMAC rate limit.

Request:

```json
{
  "email": "user@example.com"
}
```

Response:

- `202 Accepted` with an empty body for known, unknown, invalid, deleted,
  suspended, locked, or non-manual users.
- `503 unavailable` for every request when `AUTH_EMAIL_OUTBOX_ENCRYPTION_KEY` is
  missing or invalid. This check happens before user lookup.
- The response never reveals whether the e-mail exists.

Behavior:

1. Verify encrypted outbox handoff is configured.
2. Normalize e-mail.
3. For syntactically valid e-mails, generate an opaque token candidate, compute
   its domain-separated HMAC-SHA-256 hash, and run the dummy Argon2id work path
   regardless of user existence/status.
4. Only active, non-deleted, manual-auth users receive a reset token row.
5. Store only `auth.password_reset_tokens.token_hash` in the token table.
6. Supersede previous unused reset tokens for the user by setting `used_at`.
7. Insert an `auth.email_outbox` row with an encrypted `payload` envelope for a
   future worker. The raw token is present only inside AES-256-GCM ciphertext.
8. Do not return the token over HTTP.

### `POST /auth/password/reset`

Public endpoint, 4 KiB body cap, endpoint-specific IP rate limit, and secondary
submitted-token HMAC rate limit after cheap token-shape validation. Malformed
tokens use a generic target limiter key.

Request:

```json
{
  "token": "opaque-token-from-email",
  "new_password": "NewStrongPassword@123"
}
```

Response:

- `204 No Content` on success.
- `401 invalid_token` for unknown, expired, used, owner-ineligible, or otherwise
  invalid tokens.
- `400 bad_request` for weak passwords or malformed input.

Behavior:

1. Hash the submitted token with the password-reset HMAC domain prefix.
2. Validate the new password using `auth.auth_policy_settings`.
3. Hash the new password with the existing Argon2id PHC implementation.
4. In one transaction, lock the reset token row and require that its owner is
   active, not deleted, and manual-auth eligible.
5. Reject expired/used tokens generically.
6. Update the password credential, mark the token used, revoke all sessions for
   the user, and revoke active refresh-token history rows for those sessions.
7. Do not auto-login.

---

## Admin Invites

### `POST /admin/invites`

Admin endpoint guarded by `AdminBootstrapGuard` and `X-NChat-Admin-Token` until
final RBAC exists.

- Empty `ADMIN_BOOTSTRAP_TOKEN` disables the endpoint with `503`.
- Missing or wrong `X-NChat-Admin-Token` returns `401`.
- Missing or invalid `AUTH_EMAIL_OUTBOX_ENCRYPTION_KEY` returns `503` before
  user or invite lookup.
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

1. Verify encrypted outbox handoff is configured.
2. Normalize and validate e-mail and `display_name`.
3. If any user already exists with that e-mail, return `409`.
4. If an active pending invite exists, return `409`. This PR does not rotate
   pending invite tokens.
5. Generate an opaque token of at least 32 random bytes.
6. Store only a domain-separated HMAC-SHA-256 hash in
   `auth.user_invites.token_hash`.
7. Insert an `auth.email_outbox` row with an encrypted `payload` envelope for a
   future worker. The raw token is present only inside AES-256-GCM ciphertext.
8. Do not return the token over HTTP.

### `POST /auth/invites/accept`

Public endpoint, 4 KiB body cap, endpoint-specific IP rate limit, and secondary
submitted-token HMAC rate limit after cheap token-shape validation. Malformed
tokens use a generic target limiter key.

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

## Encrypted Email Outbox Handoff

`auth.email_outbox` is a handoff table only. The token tables keep only
`token_hash`. The outbox `payload` stores only this encrypted envelope:

```json
{
  "alg": "AES-256-GCM",
  "key_version": "v1",
  "nonce": "base64",
  "ciphertext": "base64"
}
```

Plaintext before encryption may include:

- `kind` (`password_reset` or `invite`)
- `token`
- `action_path` or `link_path`
- `to_email`
- `expires_at`

It must not include:

- passwords or password hashes
- access tokens or refresh tokens
- device fingerprints
- raw token links outside the encrypted ciphertext
- token hashes in payload
- e-mail bodies

`AUTH_EMAIL_OUTBOX_ENCRYPTION_KEY` is required to create password reset and
invite e-mail handoff rows. It has no default and must be standard base64 that
decodes to exactly 32 bytes, for example from `openssl rand -base64 32`. Reset
and invite-accept endpoints do not require this key because they consume tokens
already delivered to the user.

A future SMTP or notification worker can decrypt `payload` with the same
`AUTH_EMAIL_OUTBOX_ENCRYPTION_KEY`, build the user-facing e-mail link, and send
the message. That worker and real SMTP provider integration are out of scope for
this PR. This PR must not be described as completing e-mail delivery.

A database-only compromise exposes recipients and workflow metadata, but does
not expose plaintext reset or invite tokens without the outbox encryption key.
Key rotation is future work; the current envelope records `key_version: v1` so a
future worker can support multiple active/decryption-only keys.

---

## Rate Limit and Trusted Proxy Notes

Public recovery endpoints use in-memory per-process token buckets:

- `/auth/password/forgot`: endpoint-specific IP bucket plus normalized-email
  HMAC bucket.
- `/auth/password/reset`: endpoint-specific IP bucket plus submitted-token HMAC
  bucket after cheap token-shape validation.
- `/auth/invites/accept`: endpoint-specific IP bucket plus submitted-token HMAC
  bucket after cheap token-shape validation.

Limiter keys are never raw e-mail addresses or raw tokens. The IP limiter uses
`RemoteAddr` by default. When auth-service is deployed behind Traefik, set
`AUTH_TRUSTED_PROXY_CIDRS` to the Traefik pod/source CIDR; otherwise all client
traffic may share the Traefik pod IP for rate-limiting. Forwarded headers are
trusted only when `RemoteAddr` is inside the configured CIDR. The leftmost
`X-Forwarded-For` entry is used only if it parses as an IP; `X-Real-IP` is the
fallback if valid; otherwise `RemoteAddr` remains the key.

Limitations:

- The limiter is in-memory and per auth-service instance.
- Multi-replica deployments still need gateway or shared-store rate limiting.
- Target-aware buckets reduce abuse of public recovery endpoints but are not a
  replacement for distributed edge controls.

---

## Security Notes

- Reset/invite tokens are opaque random values with at least 32 bytes of entropy.
- Only HMAC-SHA-256 token hashes are persisted in token tables.
- Password reset and invite hashes use separate HMAC domain prefixes.
- Outbox handoff payloads are encrypted with AES-256-GCM and random 96-bit
  nonces.
- Token validation errors are generic to avoid token oracle behavior.
- Forgot-password responses are generic to avoid account enumeration.
- Responses do not include raw tokens, token hashes, password hashes, access
  tokens, or refresh tokens.
- Sensitive request fields, plaintext outbox payloads, ciphertext payloads, and
  e-mail bodies are not logged by these handlers.

---

## Out of Scope

- Frontend screens
- Real SMTP or transactional e-mail provider
- Notification-service worker
- OAuth/OIDC
- Final RBAC
- Auto-login after password reset or invite acceptance
- Distributed rate limiting
- Email outbox key rotation implementation

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
