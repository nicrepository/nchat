# Design: Auth Session Expiry, Password Recovery & Invites

**Date:** 2026-05-28
**Branch:** `feat/auth-session-recovery-invites`
**Target PR:** `develop`
**Requirements:** RF-46 (invite), RF-48 (password recovery), RF-51 (session expiry)

---

## Goal

Implement three related auth-service flows in a single PR:

1. **Session expiry enforcement (RF-51):** Fix the refresh flow so `idle_expires_at` is extended by `session_idle_timeout_minutes` from policy, not by the JWT refresh TTL.
2. **Password recovery (RF-48):** `POST /auth/password/forgot` and `POST /auth/password/reset` with expirable HMAC-SHA-256 tokens. Enumeration-safe responses throughout.
3. **Admin invite (RF-46):** `POST /admin/invites` (admin-only) and `POST /auth/invites/accept` (public) with expirable HMAC-SHA-256 tokens.

---

## Scope

### In scope

- Session expiry bug fix in refresh rotation (idle TTL from policy, not JWT TTL)
- `POST /auth/password/forgot` — always 202
- `POST /auth/password/reset` — 204 or safe JSON
- `POST /admin/invites` — guarded by `AdminBootstrapGuard`
- `POST /auth/invites/accept` — 201 with user summary
- Migration `000004_auth_session_recovery_invites`
- Email outbox table with encrypted token handoff payloads (see Email Outbox Decision)
- Service, store, HTTP handler tests; coverage ≥ 90%
- Runbook `docs/runbooks/task-auth-session-recovery-invites.md`
- README auth section update

### Out of scope

- Frontend screens
- Real SMTP provider or delivery worker
- OAuth/OIDC
- Final RBAC (using `AdminBootstrapGuard` temporarily)
- `notification-service` integration
- Auto-login after invite accept or password reset
- Rate-limit persistence across replicas (in-memory only)
- Email outbox worker (the table is the handoff boundary)
- AES-GCM payload encryption for outbox via `AUTH_EMAIL_OUTBOX_ENCRYPTION_KEY` (implemented in this PR; see Email Outbox Decision)

---

## Requirements Traceability

| RF    | Feature                                                   | Implementation                                                |
| ----- | --------------------------------------------------------- | ------------------------------------------------------------- |
| RF-46 | Email invite with activation link, admin-only             | `POST /admin/invites` + `POST /auth/invites/accept`           |
| RF-48 | Password recovery via expirable email token               | `POST /auth/password/forgot` + `POST /auth/password/reset`    |
| RF-51 | Session expires after inactivity, default 1h configurable | Fix `RotateRefreshToken` idle TTL; SQL checks already correct |

---

## Codebase Context

The schema is already fully prepared. All tables required exist as of migration `000001`:

| Table                        | Relevant columns                                                                                 |
| ---------------------------- | ------------------------------------------------------------------------------------------------ |
| `auth.user_sessions`         | `idle_expires_at`, `absolute_expires_at`, `revoked_at`                                           |
| `auth.password_reset_tokens` | `token_hash`, `expires_at`, `used_at`                                                            |
| `auth.user_invites`          | `token_hash`, `expires_at`, `status`, `accepted_at`, `accepted_by_user_id`, `invited_by_user_id` |
| `auth.auth_policy_settings`  | `session_idle_timeout_minutes` (default 60)                                                      |

Migration `000004` adds two policy columns and the email outbox table.

The `RotateRefreshToken` SQL already correctly rejects expired and revoked sessions:

```sql
WHERE s.revoked_at IS NULL
  AND s.idle_expires_at > now()
  AND (s.absolute_expires_at IS NULL OR s.absolute_expires_at > now())
  AND u.status = 'active'
  AND u.deleted_at IS NULL
```

The only bug is that on a successful rotation, `idle_expires_at` is set to `refreshExpiresAt` (from `TokenManager.GenerateRefreshToken()`, default 30 days) instead of `now() + session_idle_timeout_minutes` (default 60 minutes).

---

## Migration 000004

File: `migrations/auth/000004_auth_session_recovery_invites.up.sql`

```sql
BEGIN;

SET LOCAL search_path = auth, public;

-- Policy TTL fields for password reset and invite tokens
ALTER TABLE auth.auth_policy_settings
    ADD COLUMN password_reset_token_ttl_minutes INT NOT NULL DEFAULT 60,
    ADD COLUMN invite_token_ttl_hours           INT NOT NULL DEFAULT 72;

ALTER TABLE auth.auth_policy_settings
    ADD CONSTRAINT auth_policy_settings_pw_reset_ttl_check
        CHECK (password_reset_token_ttl_minutes > 0),
    ADD CONSTRAINT auth_policy_settings_invite_ttl_check
        CHECK (invite_token_ttl_hours > 0);

-- Email outbox: encrypted token handoff table for the future email worker.
-- Raw reset/invite tokens, complete token links, token hashes, passwords,
-- password hashes, access tokens, and refresh tokens are NEVER stored here.
CREATE TABLE auth.email_outbox (
    id             UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    kind           TEXT        NOT NULL,
    to_email       TEXT        NOT NULL,
    subject        TEXT        NOT NULL,
    template_key   TEXT        NOT NULL,
    reset_token_id UUID        REFERENCES auth.password_reset_tokens (id) ON DELETE CASCADE,
    invite_id      UUID        REFERENCES auth.user_invites (id) ON DELETE CASCADE,
    user_id        UUID        REFERENCES auth.users (id) ON DELETE SET NULL,
    payload        JSONB       NOT NULL DEFAULT '{}'::jsonb,
    status         TEXT        NOT NULL DEFAULT 'pending',
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    sent_at        TIMESTAMPTZ,
    attempts       INT         NOT NULL DEFAULT 0,
    last_error     TEXT,

    CONSTRAINT email_outbox_kind_check
        CHECK (kind IN ('password_reset', 'invite')),
    CONSTRAINT email_outbox_status_check
        CHECK (status IN ('pending', 'sent', 'failed')),
    CONSTRAINT email_outbox_reference_check
        CHECK (
            (kind = 'password_reset' AND reset_token_id IS NOT NULL AND invite_id IS NULL)
            OR
            (kind = 'invite' AND invite_id IS NOT NULL AND reset_token_id IS NULL)
        )
);

CREATE INDEX idx_email_outbox_status_created ON auth.email_outbox (status, created_at);
CREATE INDEX idx_email_outbox_reset_token_id ON auth.email_outbox (reset_token_id) WHERE reset_token_id IS NOT NULL;
CREATE INDEX idx_email_outbox_invite_id ON auth.email_outbox (invite_id) WHERE invite_id IS NOT NULL;

COMMIT;
```

File: `migrations/auth/000004_auth_session_recovery_invites.down.sql`

```sql
BEGIN;

SET LOCAL search_path = auth, public;

DROP TABLE IF EXISTS auth.email_outbox;

ALTER TABLE auth.auth_policy_settings
    DROP CONSTRAINT IF EXISTS auth_policy_settings_pw_reset_ttl_check,
    DROP CONSTRAINT IF EXISTS auth_policy_settings_invite_ttl_check,
    DROP COLUMN IF EXISTS password_reset_token_ttl_minutes,
    DROP COLUMN IF EXISTS invite_token_ttl_hours;

COMMIT;
```

---

## Email Outbox Decision

**Choice: encrypted outbox token handoff.**

**Rationale:** Review found that metadata-only handoff cannot support real reset/invite e-mail links because the raw token is generated, hashed, and discarded. The outbox now stores only an AES-256-GCM envelope in `payload`; token tables still store only `token_hash`. `AUTH_EMAIL_OUTBOX_ENCRYPTION_KEY` has no default and must be base64 for exactly 32 bytes. Missing or invalid key disables forgot-password and admin-invite handoff with `503` before user/invite lookup.

The encrypted plaintext can contain `kind`, `token`, `action_path` or `link_path`, `to_email`, and `expires_at`. It must not contain passwords, password hashes, access tokens, refresh tokens, device fingerprints, token hashes, plaintext token links outside ciphertext, or e-mail bodies.

This PR still does not implement SMTP or a notification worker. A future worker can decrypt the outbox envelope, build the user-facing e-mail link, and send it. Key rotation is future work; the envelope records `key_version: v1`.

**Security consequence:** A database-only compromise exposes outbox metadata and ciphertext, not plaintext reset or invite tokens without the encryption key.

---

## Part A: Session Expiry Fix

### Problem

`auth_service.go`:

```go
newRefreshToken, newRefreshHash, refreshExpiresAt, _ := s.tokens.GenerateRefreshToken()
// refreshExpiresAt = now() + refreshTTL (30 days by default)
session, err := s.store.RotateRefreshToken(ctx, oldHash, newRefreshHash, refreshExpiresAt)
```

`session_store.go`:

```sql
UPDATE auth.user_sessions
SET refresh_token_hash = $1,
    idle_expires_at = $2,   -- $2 is refreshExpiresAt (30 days!), not policy idle TTL
    last_seen_at = now()
```

`idle_expires_at` is extended by 30 days instead of `session_idle_timeout_minutes` (default 60 minutes).

### Fix

Follow the pattern in `login_store.go` (`selectLoginPolicy` within the transaction):

1. **Remove `expiresAt time.Time` from `RotateRefreshToken` interface.**
2. Inside `RotateRefreshToken`, after the `FOR UPDATE` lock, read `session_idle_timeout_minutes` from `auth.auth_policy_settings` within the same transaction.
3. Compute `idleExpiresAt = now() + session_idle_timeout_minutes * interval`.
4. Update `idle_expires_at` to `idleExpiresAt`.
5. Never update `absolute_expires_at` (which is set at login and frozen for life of session).

### Interface change

```go
// SessionStore (service layer)
type SessionStore interface {
    RotateRefreshToken(ctx context.Context, oldHash, newHash string) (domain.Session, error)
    RevokeRefreshToken(ctx context.Context, hash string) error
}
```

`AuthService.Refresh` no longer generates `refreshExpiresAt` — it only generates the raw/hash pair and passes them to the store.

### Logout idempotency

`RevokeRefreshToken` currently returns `domain.ErrInvalidRefreshToken` when the token is not found/already revoked. `AuthLogout` in `auth_handler.go` already maps this to 204. No change needed for idempotency.

---

## Part B: Password Recovery

### Domain additions (`domain/auth.go`)

```go
type ForgotPasswordInput struct {
    Email string
}

type ResetPasswordInput struct {
    Token       string
    NewPassword string
}
```

### Domain errors (`domain/errors.go`)

```go
var ErrInvalidToken = errors.New("invalid or expired token")
```

### Policy additions (`domain/user.go`)

```go
type PolicySettings struct {
    // ... existing fields ...
    PasswordResetTokenTTLMinutes int
    InviteTokenTTLHours          int
}
```

### Token helpers (`service/token_service.go`)

```go
// GenerateOpaqueToken generates 32 random bytes and returns the raw base64url token.
// Callers must hash it with the domain-specific password reset or invite helper before persistence.
func (m *TokenManager) GenerateOpaqueToken() (raw string, err error)

// HashPasswordResetToken is HMAC-SHA-256 of "nchat-password-reset-v1:" + raw.
// Domain prefix prevents cross-type hash collisions.
func (m *TokenManager) HashPasswordResetToken(raw string) string

// HashInviteToken is HMAC-SHA-256 of "nchat-invite-v1:" + raw.
func (m *TokenManager) HashInviteToken(raw string) string
```

### Service: `PasswordResetService`

```go
type PasswordResetService struct {
    tokens *TokenManager
    store  PasswordResetStore
}

func (s *PasswordResetService) ForgotPassword(ctx context.Context, input domain.ForgotPasswordInput) error
func (s *PasswordResetService) ResetPassword(ctx context.Context, input domain.ResetPasswordInput) error
```

**`ForgotPassword` flow:**

1. Normalize email.
2. Validate email format. On validation failure → return nil (same generic response, not worth leaking format rejection).
3. Fetch only active, manual, non-deleted user by email from store.
4. If no eligible user is found, run `RunDummyPasswordVerification("")` to reduce timing oracle, return nil (caller returns 202).
5. If an eligible user is found: generate opaque token via `GenerateOpaqueToken`, hash via `HashPasswordResetToken`.
6. Fetch policy for `PasswordResetTokenTTLMinutes`; compute `expiresAt = now() + TTL`.
7. Call `store.CreatePasswordResetToken(ctx, userID, email, tokenHash, expiresAt)` — this supersedes previous tokens and enqueues outbox row.
8. Return nil.

**Always return 202 from handler regardless of what the service does.**

**`ResetPassword` flow:**

1. Validate `Token` and `NewPassword` non-empty.
2. Hash token via `HashPasswordResetToken`.
3. Fetch policy via `store.GetPolicySettings`; validate `NewPassword` with `domain.ValidatePassword`.
4. Hash the new password with the existing Argon2id PHC helper in the service layer.
5. Call `store.ResetPasswordTx(ctx, tokenHash, newPasswordHash)` — all-or-nothing:
   - Find token by hash with `FOR UPDATE`; verify `used_at IS NULL`, `expires_at > now()`. On any failure: return `ErrInvalidToken`.
   - Update `auth.user_password_credentials` for the token's user.
   - Mark token `used_at = now()`.
   - Revoke all `auth.user_sessions` for the user (set `revoked_at = now()`, `revoked_reason = 'password_reset'`).
   - Revoke active `auth.refresh_token_history` rows for those sessions.
   - Commit.
6. Return nil on success.

### Store: `PasswordResetStore` interface

```go
type PasswordResetStore interface {
    GetActiveUserForPasswordReset(ctx context.Context, email string) (userID string, found bool, err error)
    GetPolicySettings(ctx context.Context) (domain.PolicySettings, error)
    CreatePasswordResetToken(ctx context.Context, userID, email, tokenHash string, expiresAt time.Time) error
    ResetPasswordTx(ctx context.Context, tokenHash, newPasswordHash string) error
}
```

**Password policy validation happens in the service layer** (consistent with `user_service.go`): `PasswordResetService.ResetPassword` calls `store.GetPolicySettings`, validates the password with `domain.ValidatePassword`, hashes it with `service.HashPassword`, then calls `store.ResetPasswordTx` with the pre-hashed value. The store never sees a plaintext password.

`CreatePasswordResetToken`:

- Within a transaction: mark all previous `password_reset_tokens` for `user_id` where `used_at IS NULL` as `used_at = now()` (supersede).
- Insert new `password_reset_tokens` row.
- Insert `email_outbox` row: `kind='password_reset'`, `reset_token_id=<new token id>`, `user_id=<user id>`, `to_email`, `template_key='auth.password_reset'`, `subject='Reset your NChat password'`.

`ResetPasswordTx`:

- Within a single transaction with `FOR UPDATE` lock on the token row.
- Validate: `used_at IS NULL AND expires_at > now()`. Return `ErrInvalidToken` if not.
- `UPDATE auth.user_password_credentials SET password_hash = $1, password_changed_at = now(), updated_at = now() WHERE user_id = (SELECT user_id FROM auth.password_reset_tokens WHERE id = <token_id>)`.
- `UPDATE auth.password_reset_tokens SET used_at = now() WHERE id = <token_id>`.
- `UPDATE auth.user_sessions SET revoked_at = now(), revoked_reason = 'password_reset' WHERE user_id = $user_id AND revoked_at IS NULL`.
- `UPDATE auth.refresh_token_history SET status = 'revoked', revoked_at = now() WHERE session_id IN (SELECT id FROM auth.user_sessions WHERE user_id = $user_id) AND status = 'active'`.
- Commit.

### HTTP: Password handler (`http/password_handler.go`)

```
POST /auth/password/forgot
  Body: {"email": "user@example.com"}
  Always 202 Accepted (empty body)
  4 KiB cap, rate-limited

POST /auth/password/reset
  Body: {"token": "...", "new_password": "..."}
  204 No Content on success
  401 {"error": "invalid_token", "message": "invalid or expired token"} on any token failure
  400 on weak password
  4 KiB cap, rate-limited
```

Neither endpoint returns token, token_hash, password_hash, or any user identifier.

---

## Part C: Admin Invite

### Domain additions (`domain/auth.go`)

```go
type AdminInviteInput struct {
    Email       string
    DisplayName string
    FullName    string
}

type AcceptInviteInput struct {
    Token       string
    DisplayName string
    FullName    string
    Password    string
}

type InviteResult struct {
    ID        string
    Email     string
    CreatedAt time.Time
}

type AcceptInviteResult struct {
    UserID      string
    Email       string
    DisplayName string
    FullName    string
    CreatedAt   time.Time
}
```

### Service: `InviteService`

```go
type InviteService struct {
    tokens *TokenManager
    store  InviteStore
}

func (s *InviteService) CreateInvite(ctx context.Context, input domain.AdminInviteInput) (domain.InviteResult, error)
func (s *InviteService) AcceptInvite(ctx context.Context, input domain.AcceptInviteInput) (domain.AcceptInviteResult, error)
```

**`CreateInvite` flow:**

1. Normalize email. Validate format and `display_name` non-empty.
2. Check user already exists → return `ErrDuplicateEmail` (caller → 409).
3. Check active pending invite exists → return `ErrInviteAlreadyPending` (caller → 409).
4. Generate opaque token; hash via `HashInviteToken`.
5. Fetch policy; compute `expiresAt = now() + InviteTokenTTLHours`.
6. Call `store.CreateInvite(ctx, ...)` transactionally (insert `user_invites` + `email_outbox`).
7. Return safe invite metadata (`id`, `email`, `created_at`). **Never return the raw token in the HTTP response.**

**`AcceptInvite` flow:**

1. Validate `Token`, `DisplayName`, `Password` non-empty.
2. Hash token via `HashInviteToken`.
3. Fetch policy via `store.GetPolicySettings`; validate `Password` with `domain.ValidatePassword`; hash with `service.HashPassword`. Return `ErrPasswordPolicy` on failure (caller → 400).
4. Call `store.AcceptInviteTx(ctx, tokenHash, displayName, fullName, passwordHash)` — all-or-nothing:
   - `FOR UPDATE` on invite row; verify `accepted_at IS NULL`, `revoked_at IS NULL`, `expires_at > now()`, `status = 'pending'`. On any failure: return `ErrInvalidToken`.
   - Insert `auth.users` with `status='active'`, `auth_source='manual'`, `email_verified_at=now()`.
   - Insert `auth.user_password_credentials`.
   - `UPDATE auth.user_invites SET accepted_at = now(), accepted_by_user_id = <new user id>, status = 'accepted'`.
   - Commit.
5. Return `AcceptInviteResult` with safe user fields.
6. **No session created. User must log in separately.**

### Domain errors (`domain/errors.go`)

```go
var ErrInviteAlreadyPending = errors.New("active invite already exists for this email")
```

### Store: `InviteStore` interface

```go
type InviteStore interface {
    UserExistsByEmail(ctx context.Context, email string) (bool, error)
    ActiveInviteExistsByEmail(ctx context.Context, email string) (bool, error)
    GetPolicySettings(ctx context.Context) (domain.PolicySettings, error)
    CreateInvite(ctx context.Context, email, displayName, fullName, tokenHash string, expiresAt time.Time) (domain.InviteResult, error)
    AcceptInviteTx(ctx context.Context, tokenHash, displayName, fullName, passwordHash string) (domain.AcceptInviteResult, error)
}
```

**Password policy validation and hashing happen in the service layer** (consistent with `user_service.go`): `InviteService.AcceptInvite` calls `store.GetPolicySettings`, validates with `domain.ValidatePassword`, hashes with `service.HashPassword`, then calls `store.AcceptInviteTx` with the pre-hashed value.

`CreateInvite`:

- Within a transaction: insert `auth.user_invites` (`invited_by_user_id = NULL`, `status = 'pending'`).
- Insert `email_outbox` row: `kind='invite'`, `invite_id=<invite id>`, `user_id=NULL`, `to_email`, `template_key='auth.invite'`, `subject='You have been invited to NChat'`.

`AcceptInviteTx`:

- `FOR UPDATE` on invite row.
- Validate invite: `status = 'pending' AND accepted_at IS NULL AND revoked_at IS NULL AND expires_at > now()`. Return `ErrInvalidToken` if not.
- Insert `auth.users` (`status='active'`, `auth_source='manual'`, `email_verified_at=now()`).
- Insert `auth.user_password_credentials`.
- `UPDATE auth.user_invites SET accepted_at = now(), accepted_by_user_id = <new user id>, status = 'accepted' WHERE id = <invite id>`.
- Commit.

### HTTP: Invite handler (`http/invite_handler.go`)

```
POST /admin/invites
  Guard: AdminBootstrapGuard (503 if token empty, 401 if wrong)
  Body: {"email": "...", "display_name": "...", "full_name": "..."}
  201 {"id": "<invite-id>", "email": "...", "created_at": "..."} — NO token
  409 if user or active invite exists
  4 KiB cap

POST /auth/invites/accept
  Body: {"token": "...", "display_name": "...", "full_name": "...", "password": "..."}
  201 {"id": "...", "email": "...", "display_name": "...", "created_at": "..."} — NO token, NO session
  401 {"error": "invalid_invite_token", "message": "invalid or expired invite"} on any token failure
  400 on weak password
  4 KiB cap, rate-limited
```

---

## Route Constants

```go
const (
    RouteAuthPasswordForgot  = "/auth/password/forgot"
    RouteAuthPasswordReset   = "/auth/password/reset"
    RouteAdminInvites        = "/admin/invites"
    RouteAuthInvitesAccept   = "/auth/invites/accept"
)
```

---

## Rate Limiting

Public endpoints get a **separate** rate limiter instance from the existing token endpoint limiter (so forgot/reset/accept do not share bucket with login/refresh):

```go
recoveryEndpointLimiter := NewTokenEndpointRateLimiter(
    cfg.AuthTokenEndpointRateLimitPerMinute,
    cfg.AuthTokenEndpointRateLimitBurst,
    cfg.AuthTrustedProxyCIDRs,
)
```

Admin endpoints (`/admin/invites`) are not rate-limited at the handler level (protected by admin token guard).

---

## Security Decisions

| Decision                       | Detail                                                                                                                                                                                |
| ------------------------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Token storage                  | Only HMAC-SHA-256 hash stored in DB (`token_hash`). Raw reset/invite tokens are never persisted and never returned by HTTP responses                                                  |
| Token domain prefix            | `HashPasswordResetToken` prefixes `nchat-password-reset-v1:`, `HashInviteToken` prefixes `nchat-invite-v1:` — prevents cross-type hash collisions                                     |
| Outbox payload                 | AES-256-GCM envelope only; token tables keep only `token_hash`; no plaintext token links, passwords, password hashes, access tokens, refresh tokens, or device fingerprints in outbox |
| Anti-enumeration               | `ForgotPassword` always returns 202; unknown/locked/deleted users run dummy path                                                                                                      |
| Generic token oracle           | Reset/accept return generic 401 for expired, used, invalid, or revoked token                                                                                                          |
| Session revocation after reset | All user sessions and active refresh history rows revoked in same transaction                                                                                                         |
| No auto-login                  | `AcceptInvite` creates user but no session                                                                                                                                            |
| Admin guard                    | `AdminBootstrapGuard` is temporary; documented as not final RBAC                                                                                                                      |
| Token logging                  | No raw token, token hash, password hash, or email body in logs/metrics/traces                                                                                                         |

---

## Test Plan

### Part A — Session expiry

| Test                                                                                  | Location                          | Type                 |
| ------------------------------------------------------------------------------------- | --------------------------------- | -------------------- |
| Active session refresh succeeds and extends `idle_expires_at` by policy TTL (not 30d) | `service/session_service_test.go` | Service (fake store) |
| Idle-expired session rejects refresh with `ErrInvalidRefreshToken`                    | `service/session_service_test.go` | Service (fake store) |
| Absolute-expired session rejects refresh                                              | `service/session_service_test.go` | Service (fake store) |
| Revoked session rejects refresh                                                       | `service/session_service_test.go` | Service (fake store) |
| Logout on unknown/expired token returns no error (handler maps to 204)                | `http/auth_handler_test.go`       | Handler              |
| Store reads policy within rotation TX, updates `idle_expires_at` correctly            | `storage/session_store_test.go`   | Store (pgxmock)      |

### Part B — Password recovery

| Test                                                                                                         | Location                                 | Type            |
| ------------------------------------------------------------------------------------------------------------ | ---------------------------------------- | --------------- |
| Known active user creates hashed token + outbox row, returns generic 202                                     | `service/password_reset_service_test.go` | Service         |
| Unknown email returns same generic response, no token created                                                | `service/password_reset_service_test.go` | Service         |
| Deleted/suspended/locked user returns same generic response                                                  | `service/password_reset_service_test.go` | Service         |
| Valid token: updates Argon2id hash, marks `used_at`, revokes sessions + token history                        | `storage/password_reset_store_test.go`   | Store (pgxmock) |
| Expired token rejected with `ErrInvalidToken`                                                                | `storage/password_reset_store_test.go`   | Store (pgxmock) |
| Used token rejected                                                                                          | `storage/password_reset_store_test.go`   | Store (pgxmock) |
| Unknown token rejected                                                                                       | `storage/password_reset_store_test.go`   | Store (pgxmock) |
| Weak password rejected with `ErrPasswordPolicy`                                                              | `service/password_reset_service_test.go` | Service         |
| Token hash stored ≠ raw token (hash does not contain raw)                                                    | `service/token_service_test.go`          | Unit            |
| Outbox envelope does not contain raw token or full token link, and decrypts to expected token under test key | `storage/password_reset_store_test.go`   | Store           |
| POST /auth/password/reset body > 4 KiB → 413                                                                 | `http/password_handler_test.go`          | Handler         |
| POST /auth/password/forgot body > 4 KiB → 413                                                                | `http/password_handler_test.go`          | Handler         |
| Rate limit exceeded → 429                                                                                    | `http/password_handler_test.go`          | Handler         |
| HTTP response never contains token_hash or password_hash                                                     | `http/password_handler_test.go`          | Handler         |

### Part C — Invites

| Test                                                                  | Location                         | Type            |
| --------------------------------------------------------------------- | -------------------------------- | --------------- |
| Admin creates invite: hashed token + outbox row created               | `service/invite_service_test.go` | Service         |
| Duplicate existing user returns 409                                   | `http/invite_handler_test.go`    | Handler         |
| Active pending invite returns 409                                     | `http/invite_handler_test.go`    | Handler         |
| Missing/wrong admin token returns 503/401                             | `http/invite_handler_test.go`    | Handler         |
| `POST /admin/invites` response never includes raw token               | `http/invite_handler_test.go`    | Handler         |
| Accept valid invite: creates user + credential, marks invite accepted | `storage/invite_store_test.go`   | Store (pgxmock) |
| Expired/used/unknown invite rejected with generic 401                 | `storage/invite_store_test.go`   | Store (pgxmock) |
| Weak password during accept rejected                                  | `service/invite_service_test.go` | Service         |
| Accept does NOT create session (no session in response)               | `http/invite_handler_test.go`    | Handler         |
| Outbox row does not contain raw token, token hash, or link            | `storage/invite_store_test.go`   | Store           |
| POST /auth/invites/accept body > 4 KiB → 413                          | `http/invite_handler_test.go`    | Handler         |
| Rate limit exceeded → 429                                             | `http/invite_handler_test.go`    | Handler         |

---

## File Map

### New

| File                                                                    | Purpose                                |
| ----------------------------------------------------------------------- | -------------------------------------- |
| `migrations/auth/000004_auth_session_recovery_invites.up.sql`           | Adds policy TTL columns + email_outbox |
| `migrations/auth/000004_auth_session_recovery_invites.down.sql`         | Removes above                          |
| `services/auth-service/internal/service/password_reset_service.go`      | ForgotPassword, ResetPassword          |
| `services/auth-service/internal/service/password_reset_service_test.go` | Service tests                          |
| `services/auth-service/internal/service/invite_service.go`              | CreateInvite, AcceptInvite             |
| `services/auth-service/internal/service/invite_service_test.go`         | Service tests                          |
| `services/auth-service/internal/storage/password_reset_store.go`        | PGXPasswordResetStore                  |
| `services/auth-service/internal/storage/password_reset_store_test.go`   | Store tests                            |
| `services/auth-service/internal/storage/invite_store.go`                | PGXInviteStore                         |
| `services/auth-service/internal/storage/invite_store_test.go`           | Store tests                            |
| `services/auth-service/internal/http/password_handler.go`               | AuthForgotPassword, AuthResetPassword  |
| `services/auth-service/internal/http/password_handler_test.go`          | Handler tests                          |
| `services/auth-service/internal/http/invite_handler.go`                 | AdminCreateInvite, AuthAcceptInvite    |
| `services/auth-service/internal/http/invite_handler_test.go`            | Handler tests                          |
| `docs/runbooks/task-auth-session-recovery-invites.md`                   | Runbook                                |

### Modified

| File                              | Change                                                                                                         |
| --------------------------------- | -------------------------------------------------------------------------------------------------------------- |
| `domain/auth.go`                  | Add `ForgotPasswordInput`, `ResetPasswordInput`, `AdminInviteInput`, `AcceptInviteInput`, `AcceptInviteResult` |
| `domain/errors.go`                | Add `ErrInvalidToken`, `ErrInviteAlreadyPending`                                                               |
| `domain/user.go`                  | Add `PasswordResetTokenTTLMinutes`, `InviteTokenTTLHours` to `PolicySettings`                                  |
| `service/token_service.go`        | Add `GenerateOpaqueToken`, `HashPasswordResetToken`, `HashInviteToken`                                         |
| `service/auth_service.go`         | Remove `expiresAt` from `RotateRefreshToken` call                                                              |
| `service/session_service_test.go` | Update for new interface; add expiry tests                                                                     |
| `storage/session_store.go`        | `RotateRefreshToken` reads policy in TX, uses idle TTL; remove `expiresAt` param                               |
| `storage/session_store_test.go`   | Update mocks for policy query; update expiry assertions                                                        |
| `storage/user_store.go`           | `GetPolicySettings` scans two new columns                                                                      |
| `storage/login_store.go`          | `selectLoginPolicy` scans two new columns                                                                      |
| `http/router.go`                  | Wire new handlers with rate limiters                                                                           |
| `http/routes.go`                  | Add 4 new route constants                                                                                      |
| `app/app.go`                      | Instantiate and wire `PasswordResetService`, `InviteService`                                                   |
| `README.md`                       | Auth section: document endpoints, RF traceability, out-of-scope                                                |

---

## Known Limitations

1. **Email delivery not implemented.** The `email_outbox` table stores encrypted token handoff payloads, but no SMTP provider or notification worker reads it in this PR. A future worker can decrypt the envelope, build the user-facing link, and send the message.

2. **Email outbox key rotation not implemented.** The envelope records `key_version: v1`, but multiple-key rotation/decryption policy is future work.

3. **Outbox AAD can be hardened further.** AES-GCM AAD currently binds envelope algorithm/version. A future worker/schema revision can additionally bind immutable row metadata such as outbox id and kind.

4. **Rate limiting is per-process, in-memory.** Does not protect multi-replica deployments. Production deployments behind a gateway should apply cluster-scoped rate limiting.

5. **`AdminBootstrapGuard` is not final RBAC.** `POST /admin/invites` is protected by a pre-shared bootstrap token. Real role-based access control is out of scope for this PR.

6. **`invited_by_user_id` is NULL.** The admin bootstrap guard has no user identity. This column will be populated once final RBAC is implemented.
