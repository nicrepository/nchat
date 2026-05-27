# Email Password Login Design

## Goal

Implement `POST /auth/login` in `auth-service` for manual users with e-mail and password credentials. The endpoint authenticates an existing user, records login attempts, applies temporary brute-force lockout, optionally registers or updates a device, creates an auth session, records the initial refresh-token history row, and returns JWT access plus opaque refresh tokens using the token service introduced in PR #215.

Tracking issue: [#53](https://github.com/nicrepository/nchat/issues/53)

Target branch: `feat/auth-email-password-login`

Target PR: `develop`

## Scope

In scope:

- `POST /auth/login`
- Argon2id PHC password verification for `auth.user_password_credentials.password_hash`
- Failed and successful login attempt audit rows in `auth.login_attempts`
- Temporary brute-force lockout based on recent `auth.login_attempts`, not `auth.users.status`
- Session creation in `auth.user_sessions`
- Initial active refresh-token history insert in `auth.refresh_token_history`
- Optional device registration/update in `auth.user_devices`
- `max_devices_per_user` enforcement when a device fingerprint is provided
- 4 KiB request body cap and per-instance in-memory rate limit for `/auth/login`
- Tests and docs

Out of scope:

- OAuth/OIDC
- Password reset e-mail
- Invite e-mail flow
- Frontend
- Final RBAC
- Device listing or device revocation
- Admin unlock flow

## Confirmed Security Decisions

Automatic brute-force protection must not set `auth.users.status = 'locked'`. That status remains reserved for a future administrative or manual lock flow.

This task uses temporary lockout based on `auth.login_attempts` and policy settings. Lockout expires naturally by time. There is no unlock endpoint or unlock UI in this task.

Login errors that can reveal identity state use the same generic client response:

- unknown e-mail
- wrong password
- temporary lockout
- deleted user
- suspended user
- administratively locked user
- existing user with no password credential
- device limit exceeded, if the request included a device fingerprint

The generic response is `401 invalid_credentials`. The implementation may use distinct internal failure reasons in `auth.login_attempts`, but must not expose them to the caller.

## Database Changes

Add migration `migrations/auth/000003_auth_login_policy_window.up.sql`:

```sql
BEGIN;

ALTER TABLE auth.auth_policy_settings
  ADD COLUMN failed_login_window_minutes INT NOT NULL DEFAULT 15,
  ADD COLUMN failed_login_lockout_minutes INT NOT NULL DEFAULT 15;

ALTER TABLE auth.auth_policy_settings
  ADD CONSTRAINT auth_policy_settings_failed_login_window_check
    CHECK (failed_login_window_minutes > 0),
  ADD CONSTRAINT auth_policy_settings_failed_login_lockout_check
    CHECK (failed_login_lockout_minutes > 0);

COMMIT;
```

Add down migration `migrations/auth/000003_auth_login_policy_window.down.sql`:

```sql
BEGIN;

ALTER TABLE auth.auth_policy_settings
  DROP CONSTRAINT IF EXISTS auth_policy_settings_failed_login_lockout_check,
  DROP CONSTRAINT IF EXISTS auth_policy_settings_failed_login_window_check,
  DROP COLUMN IF EXISTS failed_login_lockout_minutes,
  DROP COLUMN IF EXISTS failed_login_window_minutes;

COMMIT;
```

No new user lock columns are introduced. No `users.status` update is used for automatic brute-force lockout.

## Domain Model

Extend `domain.PolicySettings` with:

- `FailedLoginLimit int`
- `FailedLoginWindowMinutes int`
- `FailedLoginLockoutMinutes int`
- `SessionIdleTimeoutMinutes int`
- `MaxDevicesPerUser int`

Add login input/output types in the domain layer:

- `LoginInput`
  - `Email`
  - `Password`
  - `DeviceFingerprint`
  - `DeviceName`
  - `Platform`
  - `IPAddress`
  - `UserAgent`
- `LoginResult`
  - token fields from `domain.TokenPair`
  - safe user fields: `id`, `email`, `display_name`, `must_change_password`

Add an invalid-login sentinel error such as `domain.ErrInvalidCredentials`. It must be used for all credential, status, temporary lockout, and device-limit failures that should return a generic `401`.

## Password Verification

Add Argon2id PHC verification next to the existing password hashing helper:

- parse the existing `$argon2id$v=19$m=...,t=...,p=...$salt$hash` format
- reject malformed or unsupported hashes without leaking details
- compare derived hash with stored hash using constant-time comparison

For unknown e-mail or missing credentials, perform a dummy Argon2id verification or equivalent documented timing equalization. This does not need to be perfect in this task, but it must reduce a gross timing split between "unknown user" and "wrong password" and must be covered by a service-level test if implemented through a helper.

Passwords and password hashes must never be logged, returned, stored in attempts, added to metrics, or attached to traces.

## Architecture

Use a transactional `LoginStore`.

HTTP layer:

- `AuthLogin(login service.LoginManager)` handles request parsing, body cap, and response mapping.
- The existing token endpoint rate limiter is applied to `/auth/login` with the same config as `/auth/refresh` and `/auth/logout`.
- Invalid JSON or invalid payload returns `400`.
- Generic auth failures return `401 invalid_credentials`.
- Missing DB or token service wiring returns `503`.

Service layer:

- `LoginService` normalizes the e-mail, validates required payload fields, generates a refresh token through `TokenManager`, hashes that refresh token through `TokenManager.HashRefreshToken`, and delegates transactional persistence to `LoginStore`.
- After storage returns the session and safe user data, `LoginService` calls `TokenManager.GenerateAccessToken(userID, sessionID)`.
- `LoginService` must not duplicate JWT signing, refresh token generation, or refresh-token hashing logic already implemented by PR #215.

Storage layer:

- `PGXLoginStore` performs the full login transaction against PostgreSQL.
- It loads singleton policy settings including failed-login window, lockout duration, idle timeout, and max devices.
- It looks up the normalized e-mail and password credential.
- It calculates temporary lockout by `user_id` when the user exists and by normalized e-mail when no user exists.
- It verifies the password only for eligible active manual/password users; unknown and ineligible states still receive generic failure behavior.
- It records exactly one login attempt per request.
- It creates or updates a device only when `device_fingerprint` is non-empty.
- It creates `auth.user_sessions` and `auth.refresh_token_history` in the same transaction.
- It updates `auth.users.last_login_at` on successful login.

## Lockout Algorithm

Inputs:

- `failed_login_limit`
- `failed_login_window_minutes`
- `failed_login_lockout_minutes`
- normalized e-mail
- optional `user_id`

For an existing user:

1. Select failed attempts where `user_id = $user_id`, `success = false`, and `created_at >= now() - failed_login_window_minutes`, ordered newest first.
2. If at least `failed_login_limit` rows exist, identify the threshold-crossing row as the `failed_login_limit`th most recent failure.
3. If `threshold_crossing.created_at + failed_login_lockout_minutes > now()`, record a failed attempt with `failure_reason = 'failed_login_limit_exceeded'` and return `ErrInvalidCredentials`.
4. If the lockout duration has expired, continue to password verification. Old failures remain audit records but no longer block login.
5. If password verification fails, record a failed attempt with `failure_reason = 'invalid_credentials'`. That newly recorded failure can become the threshold-crossing row for the next request.

For an unknown e-mail:

1. Select failed attempts where `email = normalized_email`, `success = false`, and `created_at >= now() - failed_login_window_minutes`, ordered newest first.
2. Apply the same threshold-crossing and lockout-duration rule used for existing users.
3. Record either `failed_login_limit_exceeded` or `invalid_credentials` and return `ErrInvalidCredentials`.

The implementation must read and apply both policy columns. `failed_login_window_minutes` controls which failures can contribute to the threshold. `failed_login_lockout_minutes` controls how long the threshold-crossing failure blocks subsequent attempts.

Successful login records `success = true` and does not need to delete old failures. Failures expire naturally from future counts by timestamp.

## Device Handling

If `device_fingerprint` is empty:

- do not create a device row
- create the session with `device_id = NULL`

If `device_fingerprint` is present:

- trim it
- hash it before storage
- never persist or return the raw fingerprint
- upsert `auth.user_devices` for `(user_id, device_fingerprint_hash)`
- update `display_name`, `platform`, `last_ip`, and `last_seen_at`
- enforce `max_devices_per_user` before creating a new device row

If the user already has the same active fingerprint, update that device and allow login.

If creating a new device would exceed `max_devices_per_user`, record a failed login attempt with an internal failure reason such as `max_devices_exceeded` and return generic `401 invalid_credentials`.

No device listing or revocation endpoint is implemented in this task.

## Session And Token Handling

On success, the transaction creates:

- `auth.user_sessions`
  - `user_id`
  - optional `device_id`
  - `refresh_token_hash`
  - `ip_address`
  - `user_agent`
  - `idle_expires_at = now() + session_idle_timeout_minutes`
  - `absolute_expires_at = now() + AUTH_REFRESH_TOKEN_TTL_SECONDS` if the store receives this value from the generated refresh token expiry
- `auth.refresh_token_history`
  - same session ID
  - same initial refresh token hash
  - `status = 'active'`

Only the raw refresh token generated by `TokenManager.GenerateRefreshToken()` is returned to the caller. Only the hash is stored.

The access token is generated through `TokenManager.GenerateAccessToken(userID, sessionID)` after the transaction returns the session ID.

## HTTP Contract

Route: `POST /auth/login`

Request body limit: 4 KiB.

Request:

```json
{
  "email": "user@example.com",
  "password": "ChangeMe@123",
  "device_fingerprint": "optional-client-generated-stable-id",
  "device_name": "optional",
  "platform": "optional"
}
```

Response `200`:

```json
{
  "access_token": "...",
  "refresh_token": "...",
  "token_type": "Bearer",
  "expires_in": 900,
  "user": {
    "id": "...",
    "email": "user@example.com",
    "display_name": "...",
    "must_change_password": true
  }
}
```

Error responses:

- `400` invalid JSON, trailing JSON, oversized invalid payload shape, missing e-mail, missing password, invalid e-mail format
- `401` generic invalid credentials for all authentication and temporary lockout failures
- `413` request body exceeds 4 KiB
- `429` rate limit exceeded
- `500` unexpected internal error
- `503` login endpoint disabled because DB or token service wiring is unavailable

The login response is intentionally not wrapped in the shared `data` envelope if it follows the current token endpoint style. If the implementation chooses the shared envelope for consistency with admin endpoints, tests and docs must make that explicit. The preferred approach is to match `/auth/refresh` and return bare token JSON.

## Observability And Logging

Never log, return, or attach to metrics/traces:

- password
- access token
- refresh token
- password hash
- refresh token hash
- raw device fingerprint

Existing observability middleware already excludes authorization, cookies, and request bodies from traces and metrics. This endpoint must not add labels or attributes that include sensitive request fields.

Internal failure reasons are stored only in `auth.login_attempts.failure_reason`.

## Tests

Use TDD. Write failing tests before implementation.

Required unit and storage coverage:

- successful login returns access token, refresh token, token type, expiry, and safe user data
- wrong password returns `401` and records a failed login attempt
- unknown e-mail returns `401` and records a failed login attempt by normalized e-mail
- deleted, suspended, and administratively locked users return generic `401`
- temporary lockout is enforced after the failed-login threshold
- lockout does not update `users.status`
- successful login records `success = true`
- successful login updates `users.last_login_at`
- successful login creates `auth.user_sessions`
- successful login creates initial active `auth.refresh_token_history`
- session row stores only `refresh_token_hash`, not raw refresh token
- response never contains `password`, `password_hash`, `refresh_token_hash`, or raw `device_fingerprint`
- device fingerprint is hashed before storage
- no device row is created when `device_fingerprint` is empty
- existing device fingerprint updates the device instead of creating a duplicate
- `max_devices_per_user` is enforced or any partial limitation is explicitly documented before PR review
- oversized login request returns `413`
- login route rate limit returns `429`
- invalid JSON and trailing JSON return `400`
- token service or DB unavailable returns `503`
- coverage remains at or above 90%

## Documentation

Create `docs/runbooks/task-email-password-login.md` with:

- endpoint contract
- environment variables inherited from JWT token service
- failed-login policy columns and defaults
- temporary lockout by time window
- explicit statement that `users.status = 'locked'` is reserved for future administrative/manual lock
- no unlock flow in this task
- no OAuth/OIDC, password reset, invite e-mail, frontend, or final RBAC
- rate-limit limitation: in-memory, per instance, until Valkey or gateway-level rate limiting exists
- traceability to RF-47, RF-49, RF-50, RF-51, and RF-52/RF-53 foundation

Update `README.md` auth section:

- add `/auth/login` to auth-service endpoints
- add a short "Email/password login" subsection
- link the new runbook

## Manual Integration

If Docker and local services are available:

```bash
make dev-env-up
make migrations-up
```

Run auth-service with:

```bash
DATABASE_URL=...
AUTH_JWT_HMAC_SECRET=...
ADMIN_BOOTSTRAP_TOKEN=...
```

Manual exercise:

1. Create a user via `POST /admin/users`.
2. Login via `POST /auth/login`.
3. Call `POST /auth/refresh` with the returned refresh token.
4. Call `POST /auth/logout`.
5. Verify duplicate/wrong password and lockout cases.

## Validation

Required validation before completion:

```bash
pnpm format:check
pnpm run ci
make ci
```

Also run focused auth-service checks during development:

```bash
cd services/auth-service && go test ./...
pnpm migrations:check
pnpm test:coverage:go:check
```

Because this touches public auth endpoints, run code review and security review before declaring completion. Use Semgrep when available.

## Spec Self-Review

Placeholder scan: passed. No TBD or open implementation blanks remain.

Consistency check: passed. The design consistently uses transactional login persistence and temporary lockout based on `auth.login_attempts`, not `users.status`.

Scope check: passed. The work is a single auth-service feature with one small migration and docs updates.

Ambiguity check: passed. Lockout, unknown-user behavior, device behavior, token reuse, response genericity, and out-of-scope items are explicit.
