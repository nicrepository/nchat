# Design: Device and Session Management Endpoints

**Date:** 2026-05-29
**Status:** Approved
**RF traceability:** RF-51, RF-52, RF-53
**Out of scope:** RF-54 (new-device notifications), frontend UI, admin RBAC for devices, MFA

---

## 1. Goals

Implement authenticated REST endpoints so users can inspect and revoke their own sessions and devices, satisfying:

- **RF-51** — sessions expire after inactivity (idle/absolute expiry enforced at DB level; endpoints surface those fields)
- **RF-52** — multi-device limit is configurable by admin (`auth_policy_settings.max_devices_per_user`); device list exposes this limit
- **RF-53** — users can list and revoke linked devices and sessions

---

## 2. Approach: Dedicated Service (A)

New layered components that mirror the `LoginAttemptsService` pattern from PR #219:

```
bearer_middleware  ← inject ctxKeySessionID alongside ctxKeyUserID
    ↓
session_handler / device_handler   (internal/http/)
    ↓
DeviceSessionManager interface     (internal/service/)
    ↓
DeviceSessionService               (internal/service/)
    ↓
DeviceSessionStore interface       (internal/service/)
    ↓
PGXDeviceSessionStore              (internal/storage/)
    ↓
PostgreSQL auth.user_sessions / auth.user_devices / auth.refresh_token_history
```

---

## 3. Endpoints

| Method | Path                           | Auth   | Description                                                                                                |
| ------ | ------------------------------ | ------ | ---------------------------------------------------------------------------------------------------------- |
| GET    | /auth/me/sessions              | Bearer | List own sessions, newest first; active only by default; `?include_revoked=true` for history               |
| DELETE | /auth/me/sessions/{session_id} | Bearer | Revoke one own session                                                                                     |
| DELETE | /auth/me/sessions              | Bearer | Revoke all sessions **except current**; requires valid `sid` and active DB session; returns 401 if invalid |
| GET    | /auth/me/devices               | Bearer | List own devices; active only by default; `?include_revoked=true`                                          |
| DELETE | /auth/me/devices/{device_id}   | Bearer | Revoke device + its sessions                                                                               |
| PATCH  | /auth/me/devices/{device_id}   | Bearer | Update `display_name` (1–80 chars); active devices only                                                    |

### Session response fields (never expose `refresh_token_hash`, `device_fingerprint_hash`, raw IP)

```json
{
  "id": "uuid",
  "device_id": "uuid|null",
  "created_at": "RFC3339",
  "last_seen_at": "RFC3339",
  "idle_expires_at": "RFC3339",
  "absolute_expires_at": "RFC3339|null",
  "revoked_at": "RFC3339|null",
  "ip_address": "1.2.*.*",
  "user_agent": "Mozilla/5.0 … (truncated 200 chars)",
  "current": true
}
```

### Device response fields

```json
{
  "id": "uuid",
  "display_name": "string|null",
  "platform": "string|null",
  "last_ip": "1.2.*.*",
  "first_seen_at": "RFC3339",
  "last_seen_at": "RFC3339",
  "revoked_at": "RFC3339|null",
  "session_count": 2,
  "current": false
}
```

Device list also includes `meta.max_devices_per_user` from `auth_policy_settings`.

---

## 4. Context Extension — BearerAuth

`bearer_middleware.go` extended to inject both context keys:

```go
const (
  ctxKeyUserID    ctxKey = iota
  ctxKeySessionID        // new: carries AccessClaims.SessionID ("sid")
)
```

`TokenManager.ValidateAccessToken` rejects access tokens that do not carry `sid`. If a handler is called with a bypassed context and no `ctxKeySessionID`, bulk revoke returns `401` and never calls `RevokeAllSessionsExcept(userID, "")`.

The device/session routes also run DB-backed current-session validation after `BearerAuth`: `sid` must belong to `sub`, be unrevoked, not idle-expired, not absolute-expired, and belong to an active, non-deleted user.

---

## 4b. Path Parameter Validation

All handlers with `{session_id}` or `{device_id}` path parameters **validate the UUID format** before touching storage:

```
if !isValidUUID(sessionID) { return 400 "bad_request" }
```

An invalid UUID format returns **400**, not 404 or 500, and never reaches the SQL layer. `currentSessionID == ""` is treated as unknown-session (not an error) by `ListDevices` — the subquery returns NULL and `current=false` for all devices.

---

## 5. Domain Types

Additions to `internal/domain/auth.go` (`SessionInfo`, `DeviceInfo`, `DeviceSessionPolicy`).
`ErrNotFound` added to `internal/domain/errors.go` alongside existing sentinels.

```go
// SessionInfo is the safe, displayable representation of a user session.
type SessionInfo struct {
    ID                string
    DeviceID          *string
    CreatedAt         time.Time
    LastSeenAt        time.Time
    IdleExpiresAt     time.Time
    AbsoluteExpiresAt *time.Time
    RevokedAt         *time.Time
    IPAddress         string   // raw; masking happens in HTTP layer
    UserAgent         string   // raw; sanitizing happens in HTTP layer
}

// DeviceInfo is the safe, displayable representation of a linked device.
type DeviceInfo struct {
    ID           string
    DisplayName  *string
    Platform     *string
    LastIP       string    // raw; masking happens in HTTP layer
    FirstSeenAt  time.Time
    LastSeenAt   time.Time
    RevokedAt    *time.Time
    SessionCount int
    Current      bool       // true when current access token's session belongs to this device
}

// DeviceSessionPolicy carries the policy fields needed by device/session endpoints.
type DeviceSessionPolicy struct {
    MaxDevicesPerUser int
}
```

---

## 6. Service Layer (internal/service/device_session_service.go)

```go
// DeviceSessionStore is the persistence interface for the service.
type DeviceSessionStore interface {
    ListSessions(ctx, userID string, limit int) ([]domain.SessionInfo, error)
    RevokeSession(ctx, sessionID, userID string) error          // 404 if not found/cross-user; idempotent (already revoked → nil)
    RevokeAllSessionsExcept(ctx, userID, exceptSessionID string) error
    ListDevices(ctx, userID, currentSessionID string, includeRevoked bool, limit int) ([]domain.DeviceInfo, domain.DeviceSessionPolicy, error)
    RevokeDevice(ctx, deviceID, userID string) error            // 404 if not found/cross-user; cascades to sessions + refresh_token_history
    UpdateDeviceDisplayName(ctx, deviceID, userID, name string) error
}

// DeviceSessionManager is the HTTP-facing interface.
type DeviceSessionManager interface {
    ListSessions(ctx, userID string, limit int) ([]domain.SessionInfo, error)
    RevokeSession(ctx, sessionID, userID string) error
    RevokeAllSessionsExcept(ctx, userID, exceptSessionID string) error
    ListDevices(ctx, userID, currentSessionID string, includeRevoked bool, limit int) ([]domain.DeviceInfo, domain.DeviceSessionPolicy, error)
    RevokeDevice(ctx, deviceID, userID string) error
    UpdateDeviceDisplayName(ctx, deviceID, userID, name string) error
}
```

Errors used: `domain.ErrNotFound` (new sentinel added to `internal/domain/errors.go`), `domain.ErrInvalidInput` (existing).

---

## 7. Storage Queries (PGXDeviceSessionStore)

### ListSessions

```sql
SELECT id, device_id, created_at, last_seen_at,
       idle_expires_at, absolute_expires_at, revoked_at,
       ip_address::text, user_agent
FROM auth.user_sessions
WHERE user_id = $1
ORDER BY created_at DESC, id DESC
LIMIT $2
```

### RevokeSession (transaction)

```sql
-- 1. Lock + existence check
SELECT id FROM auth.user_sessions
WHERE id = $1 AND user_id = $2
FOR UPDATE
-- ErrNotFound if no rows

-- 2. Revoke session (idempotent via WHERE revoked_at IS NULL; not found after lock = already revoked → still 204)
UPDATE auth.user_sessions
SET revoked_at = now(), revoked_reason = 'user_revoked'
WHERE id = $1 AND user_id = $2 AND revoked_at IS NULL

-- 3. Revoke active refresh token history
UPDATE auth.refresh_token_history
SET status = 'revoked', revoked_at = now()
WHERE session_id = $1 AND status = 'active'
```

### RevokeDevice (single transaction — CTE avoids separate collect step)

```sql
-- 1. Lock + existence check (ErrNotFound if no rows)
SELECT id FROM auth.user_devices WHERE id = $1 AND user_id = $2 FOR UPDATE;

-- 2–5. CTE: revoke device, cascade to sessions and their refresh token history atomically.
-- Handles device with zero active sessions without array-type issues.
WITH revoked_device AS (
    UPDATE auth.user_devices
    SET revoked_at = now()
    WHERE id = $1 AND user_id = $2 AND revoked_at IS NULL
),
revoked_sessions AS (
    UPDATE auth.user_sessions
    SET revoked_at = now(), revoked_reason = 'device_revoked'
    WHERE device_id = $1 AND user_id = $2 AND revoked_at IS NULL
    RETURNING id
)
UPDATE auth.refresh_token_history
SET status = 'revoked', revoked_at = now()
WHERE session_id IN (SELECT id FROM revoked_sessions)
  AND status = 'active';
```

Test: device with zero active sessions must return 204 (no rows in revoked_sessions is fine — the CTE still executes).

### ListDevices

```sql
-- $1=user_id, $2=current_session_id (may be empty string → NULL-safe), $3=include_revoked, $4=limit
SELECT d.id, d.display_name, d.platform,
       d.last_ip::text, d.first_seen_at, d.last_seen_at, d.revoked_at,
       COUNT(s.id) FILTER (WHERE s.revoked_at IS NULL) AS session_count,
       CASE WHEN $2 <> '' THEN
           COALESCE(d.id = (SELECT device_id FROM auth.user_sessions WHERE id = $2::uuid AND user_id = $1), false)
       ELSE false END AS current
FROM auth.user_devices AS d
LEFT JOIN auth.user_sessions AS s ON s.device_id = d.id AND s.user_id = d.user_id
WHERE d.user_id = $1
  AND ($3 OR d.revoked_at IS NULL)
GROUP BY d.id
ORDER BY d.last_seen_at DESC, d.id DESC
LIMIT $4
```

When `currentSessionID == ""` the `CASE` returns `false` for all rows — no UUID cast attempted.

Policy fetched in a separate query:

```sql
SELECT max_devices_per_user FROM auth.auth_policy_settings WHERE id = 1
```

---

## 8. Migration (000006)

File: `migrations/auth/000006_device_session_indexes.up.sql`

Existing indexes in migration 000001:

- `idx_user_sessions_user_revoked (user_id, revoked_at)` — exists
- `idx_user_sessions_user_device (user_id, device_id)` — exists
- `idx_user_devices_user_revoked (user_id, revoked_at)` — exists

New compound indexes for efficient ordering and device-cascade queries:

```sql
-- Ordering sessions newest-first per user (not covered by existing indexes)
CREATE INDEX idx_user_sessions_user_created
    ON auth.user_sessions (user_id, created_at DESC, id DESC);

-- Device revocation cascade: find active sessions by (user, device) quickly.
-- Partial index avoids indexing NULL device_id rows.
-- Distinct from existing idx_user_sessions_user_device (user_id, device_id) which lacks revoked_at.
CREATE INDEX idx_user_sessions_user_device_revoked
    ON auth.user_sessions (user_id, device_id, revoked_at)
    WHERE device_id IS NOT NULL;

-- Ordering devices by last_seen newest-first per user (not covered by existing indexes)
CREATE INDEX idx_user_devices_user_last_seen
    ON auth.user_devices (user_id, last_seen_at DESC, id DESC);
```

Down migration drops only those three indexes.

---

## 9. Privacy & Security Invariants

- **SQL-level**: every query has `WHERE user_id = $authenticated_user_id` — cross-user access impossible at DB level.
- **Never returned**: `device_fingerprint_hash`, `refresh_token_hash`, `password_hash`, raw tokens, auth headers, internal policy rows.
- **IP masking**: reuse `maskIPAddress()` from `login_attempts_handler.go` (same package).
- **User-agent**: reuse `sanitizeUserAgent()` (truncated to 200 chars, non-printable stripped).
- **PATCH display_name**: trimmed, 1–80 chars, CR/LF/NUL stripped, cannot touch fingerprint/hash/revoked_at/platform. **Only active (non-revoked) devices**: revoked, cross-user, or not-found device → 404.
- **Parameterized SQL only** — no string formatting in queries.
- **No logging** of tokens, hashes, fingerprints, raw IPs, or raw user-agents.

---

## 9b. Revocation vs. Access Token Validity

`BearerAuth` remains stateless globally: it validates JWT signature and expiry and injects `sub`/`sid`. The device/session management routes add a scoped DB-backed current-session guard, so a revoked or expired current session is rejected on these routes before handler logic runs.

- A revoked session's access token may still validate cryptographically until `exp`, but it cannot access `/auth/me/sessions` or `/auth/me/devices` once the backing DB session is no longer active.
- Other authenticated endpoints keep the existing stateless `BearerAuth` behavior unless they opt into a DB-backed guard later.

---

## 10. Idempotency Decisions

| Operation                     | Already-own, already-revoked | Cross-user / not found |
| ----------------------------- | ---------------------------- | ---------------------- |
| DELETE /auth/me/sessions/{id} | 204                          | 404                    |
| DELETE /auth/me/sessions      | 204 (or 204 with 0 revoked)  | n/a (bulk, own only)   |
| DELETE /auth/me/devices/{id}  | 204                          | 404                    |
| PATCH /auth/me/devices/{id}   | 404 (revoked device)         | 404                    |

`DELETE /auth/me/sessions` returns `401` when the access token has no `sid` or the current DB session is invalid, and the bulk revoke call is not reached.

---

## 11. Pagination

All list endpoints accept `?limit=` (default 50, max 100). No cursor pagination (sessions/devices are small sets). Matches `GetMyLoginAttempts` pattern.

---

## 12. current=true Logic

`current=true` on a session when `session.ID == ctxKeySessionID` from the JWT `sid` claim.
`current=true` on a device when `device.ID == session.DeviceID` for the current session.
Access tokens without `sid` are rejected before these handlers run.

---

## 13. Files to Create / Modify

### New files

| File                                                                    | Purpose                                            |
| ----------------------------------------------------------------------- | -------------------------------------------------- |
| `migrations/auth/000006_device_session_indexes.{up,down}.sql`           | Performance indexes                                |
| `services/auth-service/internal/domain/auth.go` (additions)             | `SessionInfo`, `DeviceInfo`, `DeviceSessionPolicy` |
| `services/auth-service/internal/domain/errors.go` (addition)            | `ErrNotFound` sentinel                             |
| `services/auth-service/internal/storage/device_session_store.go`        | `PGXDeviceSessionStore`                            |
| `services/auth-service/internal/storage/device_session_store_test.go`   | Storage unit tests                                 |
| `services/auth-service/internal/service/device_session_service.go`      | `DeviceSessionService`, interfaces                 |
| `services/auth-service/internal/service/device_session_service_test.go` | Service unit tests                                 |
| `services/auth-service/internal/http/session_handler.go`                | Session HTTP handlers                              |
| `services/auth-service/internal/http/session_handler_test.go`           | Session handler tests                              |
| `services/auth-service/internal/http/device_handler.go`                 | Device HTTP handlers                               |
| `services/auth-service/internal/http/device_handler_test.go`            | Device handler tests                               |
| `docs/runbooks/task-device-session-management.md`                       | Runbook + RF traceability                          |

### Modified files

| File                                                       | Change                                            |
| ---------------------------------------------------------- | ------------------------------------------------- |
| `services/auth-service/internal/http/bearer_middleware.go` | Inject `ctxKeySessionID`                          |
| `services/auth-service/internal/http/routes.go`            | Add 6 new route constants                         |
| `services/auth-service/internal/http/router.go`            | Register new handlers                             |
| `services/auth-service/internal/app/app.go`                | Wire `DeviceSessionService`                       |
| `README.md`                                                | Update auth section with new endpoints + RF table |

---

## 14. Out of Scope

- Frontend UI
- Admin RBAC device management
- RF-54: push/email notification for new device (do not implement)
- MFA
- Impossible device fingerprint enforcement for sessions without fingerprint
- Hard-delete / anonymization of session/device records
