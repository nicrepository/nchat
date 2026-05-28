# Runbook: Email/Password Login (`POST /auth/login`)

## Overview

This runbook covers the `POST /auth/login` endpoint added in the
`feat/auth-email-password-login` branch. The endpoint authenticates manual
email/password users, enforces a temporary failed-login lockout, optionally
tracks devices, creates a new session with a fresh refresh-token, and returns
an access/refresh token pair.

---

## Endpoint

| Field        | Value                                                                                                                                                     |
| ------------ | --------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Path         | `POST /auth/login`                                                                                                                                        |
| Auth         | None (public)                                                                                                                                             |
| Rate limit   | Shared with `/auth/refresh` and `/auth/logout` — `AUTH_TOKEN_ENDPOINT_RATE_LIMIT_PER_MINUTE` / burst `AUTH_TOKEN_ENDPOINT_RATE_LIMIT_BURST` per client IP |
| Body cap     | 4 KiB                                                                                                                                                     |
| Content-Type | `application/json`                                                                                                                                        |

### Request body

```json
{
  "email": "user@example.com",
  "password": "ChangeMe@123",
  "device_fingerprint": "<optional-client-fingerprint>",
  "device_name": "Laptop",
  "platform": "linux"
}
```

- `email` and `password` are required; all other fields are optional.
- The email is normalized (lowercased, trimmed) before lookup.
- The password is verified via Argon2id against the stored PHC hash; it is
  never logged or persisted.

### Success response (`200 OK`)

```json
{
  "access_token": "<HS256-JWT>",
  "refresh_token": "<opaque-base64url>",
  "token_type": "Bearer",
  "expires_in": 900,
  "user": {
    "id": "<uuid>",
    "email": "user@example.com",
    "display_name": "User Name",
    "must_change_password": false
  }
}
```

### Error responses

| Status | Code                  | Cause                                                     |
| ------ | --------------------- | --------------------------------------------------------- |
| 400    | `bad_request`         | Malformed JSON, trailing garbage, missing required fields |
| 401    | `invalid_credentials` | Wrong password, unknown email, suspended user, or lockout |
| 413    | `request_too_large`   | Body exceeds 4 KiB                                        |
| 429    | `too_many_requests`   | Rate limit exceeded                                       |
| 500    | `internal_error`      | Unexpected server error (token generation, DB write)      |
| 503    | `service_unavailable` | Database or token manager not configured                  |

---

## Temporary Lockout

Failed login attempts are recorded in `auth.login_attempts`. Only
**credential failures** (`invalid_credentials`, `unknown_user`,
`invalid_password`) count toward the brute-force threshold; lockout-denied
attempts (`failed_login_limit_exceeded`), device errors (`device_revoked`,
`max_devices_exceeded`), and other non-credential reasons are excluded.

When the number of credential failures within `failed_login_window_minutes`
reaches `failed_login_limit`, subsequent attempts return `401
invalid_credentials` with failure reason `failed_login_limit_exceeded` recorded
internally. The lockout check uses a lookback of
`failed_login_window_minutes + failed_login_lockout_minutes` so that a
threshold-crossing group remains detectable even after the shorter detection
window expires.

The lockout expires automatically after `failed_login_lockout_minutes` from the
threshold-crossing failure (the Nth credential failure that completed a group).
There is **no unlock endpoint and no admin unlock UI** — lockout expires by
policy time only.

> **Important:** Automatic brute-force lockout does **not** set
> `auth.users.status = 'locked'`. That status is reserved for a future
> administrative/manual lock flow. This task has no unlock endpoint or unlock
> UI; lockout expires by policy time.

### Policy columns (table: `auth.auth_policy_settings`)

| Column                         | Default | Description                                            |
| ------------------------------ | ------- | ------------------------------------------------------ |
| `failed_login_limit`           | 5       | Max failures before temporary lockout                  |
| `failed_login_window_minutes`  | 15      | Rolling window (minutes) for counting failures         |
| `failed_login_lockout_minutes` | 15      | How long the lockout lasts (same as window by default) |
| `session_idle_timeout_minutes` | 60      | Session idle TTL used for `idle_expires_at`            |
| `max_devices_per_user`         | 5       | Max active (non-revoked) devices per user              |

---

## Device Handling

If `device_fingerprint` is provided in the request:

1. The raw fingerprint is **HMAC-SHA256** hashed in `LoginService` using
   the token manager HMAC secret with device-fingerprint domain separation.
   The raw value is never stored.
2. The store looks up `auth.user_devices` by `(user_id, device_fingerprint_hash)`
   **regardless of revocation state** to avoid unique-constraint violations on
   the non-partial `(user_id, device_fingerprint_hash)` index.
3. If found and **revoked** (`revoked_at IS NOT NULL`):
   - The login fails with `401 invalid_credentials`.
   - A `device_revoked` failure reason is recorded in `auth.login_attempts`.
   - The revoked device is not reactivated. Reactivation is out of scope for this task.
4. If found and **active** (`revoked_at IS NULL`):
   - The device's `display_name`, `platform`, `last_ip`, and `last_seen_at`
     are updated; the `device_id` is linked to the new session.
5. If not found:
   - Active (non-revoked) device count is checked against `max_devices_per_user`.
   - If the limit is already reached, the login fails with `invalid_credentials`
     and `max_devices_exceeded` is recorded.
   - Otherwise a new device row is inserted.

If `device_fingerprint` is **omitted**, `device_id` on the session is `NULL`.
`max_devices_per_user` applies **only to device-bound (fingerprinted) logins**
in this PR. Sessions created without a fingerprint are not counted against the
limit. Full per-session device enforcement requires client-side device
fingerprinting and a future device management UX.

### Trusted proxy configuration

When `AUTH_TRUSTED_PROXY_CIDRS` is configured (e.g. `10.0.0.0/8`):

- Rate-limiting uses the leftmost entry of `X-Forwarded-For` (or `X-Real-IP`
  as fallback) **only when** the direct `RemoteAddr` is inside a configured
  trusted CIDR **and** the forwarded value passes `net.ParseIP` validation.
  Canonicalized (`parsed.String()`) IPs are used as limiter keys; raw header
  strings are never used directly.
- Empty, malformed, or non-IP forwarded-header values fall back to `RemoteAddr`.
- **Traefik / reverse proxy must overwrite or sanitize inbound
  `X-Forwarded-For`** before forwarding to auth-service; auth-service must
  not be directly reachable from the internet when trusted proxy mode is
  enabled.

---

## Environment Variables

| Variable                                    | Purpose                                                                                                                                                                                                                                   |
| ------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `DATABASE_URL`                              | PostgreSQL DSN; login endpoint disabled if absent                                                                                                                                                                                         |
| `AUTH_JWT_HMAC_SECRET`                      | Min 32-byte HMAC secret for token signing                                                                                                                                                                                                 |
| `AUTH_JWT_ISSUER`                           | JWT `iss` claim                                                                                                                                                                                                                           |
| `AUTH_JWT_AUDIENCE`                         | JWT `aud` claim                                                                                                                                                                                                                           |
| `AUTH_ACCESS_TOKEN_TTL_SECONDS`             | Access token TTL (default 900 s = 15 min)                                                                                                                                                                                                 |
| `AUTH_REFRESH_TOKEN_TTL_SECONDS`            | Refresh token TTL (default 2592000 s = 30 days)                                                                                                                                                                                           |
| `AUTH_TOKEN_ENDPOINT_RATE_LIMIT_PER_MINUTE` | Requests/min per IP shared across token endpoints                                                                                                                                                                                         |
| `AUTH_TOKEN_ENDPOINT_RATE_LIMIT_BURST`      | Burst capacity for the rate limiter                                                                                                                                                                                                       |
| `AUTH_TRUSTED_PROXY_CIDRS`                  | Comma-separated CIDRs of trusted reverse proxies (e.g. `10.0.0.0/8`); when the request RemoteAddr is in this list, `X-Forwarded-For` first IP is used for rate-limiting. Empty = use RemoteAddr only (default, safe for single-instance). |

---

## Security Notes

- **Credential isolation:** Argon2id PHC hashes are stored in
  `auth.user_password_credentials`, never in `auth.users`. The raw password
  never leaves the `VerifyPassword` call.
- **Token hashing:** Refresh tokens are stored as HMAC-SHA256 digests in both
  `auth.user_sessions.refresh_token_hash` and `auth.refresh_token_history`.
  The raw token is returned only to the caller.
- **User enumeration prevention:** Unknown email, ineligible user states, and
  users without password credentials run a dummy Argon2id operation
  (`RunDummyPasswordVerification`) to reduce timing differences from the
  normal password-verification path.
- **Generic error response:** All credential failures (wrong password, unknown
  email, suspended user, lockout) map to the same `401 invalid_credentials`
  response to prevent enumeration.
- **No lockout status change:** Temporary lockout never modifies
  `auth.users.status`; the `locked` status is reserved for explicit
  administrative actions.

---

## Known Limitations

- No email verification is enforced at login time (`email_verified_at` is not
  checked). A verified-email gate is out of scope for this task.
- No TOTP or second-factor challenge. MFA is out of scope.
- No unlock endpoint for the temporary lockout window.
- No CAPTCHA integration.
- Rate limiting is in-memory and per auth-service instance until Valkey or gateway-level limiting is added.
- Device revocation UI is out of scope.

---

## Manual Integration

1. Apply migration 000003:
   ```bash
   psql "$DATABASE_URL" -f migrations/auth/000003_auth_login_policy_window.up.sql
   ```
2. Start the auth-service with a valid `DATABASE_URL` and JWT config.
3. Send a test login:
   ```bash
   curl -s -X POST http://localhost:8081/auth/login \
     -H 'Content-Type: application/json' \
     -d '{"email":"admin@example.com","password":"ChangeMe@123"}' | jq .
   ```
4. Expect `200` with `access_token`, `refresh_token`, and `user` fields.

---

## Validation

Run the full auth-service test suite:

```bash
cd services/auth-service && go test ./...
pnpm test:coverage:go:check
pnpm run ci
make ci
```

---

## Related and Traceability

### Requirements Framework (RF)

| ID          | Title                            |
| ----------- | -------------------------------- |
| RF-47       | Password policy foundation       |
| RF-49       | Brute-force lockout              |
| RF-50       | Login attempts log foundation    |
| RF-51       | Session idle expiration          |
| RF-52/RF-53 | Multi-device / device foundation |

### Files and Documents

| Item            | Reference                                                           |
| --------------- | ------------------------------------------------------------------- |
| Plan            | `docs/superpowers/plans/2026-05-27-auth-email-password-login.md`    |
| Migration       | `migrations/auth/000003_auth_login_policy_window.up.sql`            |
| Login service   | `services/auth-service/internal/service/login_service.go`           |
| Login store     | `services/auth-service/internal/storage/login_store.go`             |
| HTTP handler    | `services/auth-service/internal/http/auth_handler.go` (`AuthLogin`) |
| Refresh runbook | `docs/runbooks/task-jwt-access-refresh.md`                          |
