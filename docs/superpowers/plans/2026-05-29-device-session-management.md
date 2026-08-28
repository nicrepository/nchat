# Device and Session Management Endpoints — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add six authenticated REST endpoints to `auth-service` so users can list and revoke their own sessions and linked devices, satisfying RF-51, RF-52, and RF-53.

**Architecture:** New `DeviceSessionService` + `PGXDeviceSessionStore` follow the same layered pattern as `LoginAttemptsService` from PR #219. `BearerAuth` middleware is extended to inject `ctxKeySessionID` from JWT `sid` claim alongside the existing `ctxKeyUserID`. Two handler files (`session_handler.go`, `device_handler.go`) each define the narrow interface they need, both satisfied by `*DeviceSessionService`.

**Tech Stack:** Go 1.25, `net/http` (stdlib mux with `{param}` patterns), pgx v5, pgxmock v2 for storage tests, `github.com/golang-jwt/jwt/v5`.

**Branch:** `feat/auth-device-session-management` (already created, design doc committed)

---

## File Map

| File                                                                    | Action | Responsibility                                                                 |
| ----------------------------------------------------------------------- | ------ | ------------------------------------------------------------------------------ |
| `migrations/auth/000006_device_session_indexes.{up,down}.sql`           | Create | Compound indexes for list ordering and device cascade                          |
| `services/auth-service/internal/domain/errors.go`                       | Modify | Add `ErrNotFound` sentinel                                                     |
| `services/auth-service/internal/domain/auth.go`                         | Modify | Add `SessionInfo`, `DeviceInfo`, `DeviceSessionPolicy`                         |
| `services/auth-service/internal/http/bearer_middleware.go`              | Modify | Inject `ctxKeySessionID` into context                                          |
| `services/auth-service/internal/http/bearer_middleware_test.go`         | Modify | Add test: `sid` injected into context                                          |
| `services/auth-service/internal/storage/device_session_store.go`        | Create | `PGXDeviceSessionStore` — 6 methods                                            |
| `services/auth-service/internal/storage/device_session_store_test.go`   | Create | pgxmock-based unit tests for each method                                       |
| `services/auth-service/internal/service/device_session_service.go`      | Create | `DeviceSessionStore` interface, `DeviceSessionService`, `DeviceSessionManager` |
| `services/auth-service/internal/service/device_session_service_test.go` | Create | Fake-store unit tests for validation logic                                     |
| `services/auth-service/internal/http/session_handler.go`                | Create | `SessionManager` interface, GET/DELETE session handlers                        |
| `services/auth-service/internal/http/session_handler_test.go`           | Create | Handler tests (auth, isolation, privacy, idempotency)                          |
| `services/auth-service/internal/http/device_handler.go`                 | Create | `DeviceManager` interface, GET/DELETE/PATCH device handlers                    |
| `services/auth-service/internal/http/device_handler_test.go`            | Create | Handler tests (auth, isolation, privacy, PATCH validation)                     |
| `services/auth-service/internal/http/routes.go`                         | Modify | Add 6 route constants                                                          |
| `services/auth-service/internal/http/router.go`                         | Modify | Add `sessions SessionManager, devices DeviceManager` params; wire handlers     |
| `services/auth-service/internal/http/router_test.go`                    | Modify | Add 2 `nil` args to all `NewRouter(...)` calls                                 |
| `services/auth-service/internal/app/app.go`                             | Modify | Create and wire `DeviceSessionService`                                         |
| `docs/runbooks/task-device-session-management.md`                       | Create | Runbook: RF traceability, endpoints, security, out of scope                    |
| `README.md`                                                             | Modify | Update auth endpoints section with new routes + RF table                       |

---

## Task 1: Migration — Compound Indexes

**Files:**

- Create: `migrations/auth/000006_device_session_indexes.up.sql`
- Create: `migrations/auth/000006_device_session_indexes.down.sql`

- [ ] **Step 1: Create the up migration**

```sql
-- migrations/auth/000006_device_session_indexes.up.sql
-- Adds compound indexes to support efficient ordering and device-cascade revocation
-- for the device/session management endpoints (RF-51, RF-52, RF-53).
--
-- Existing indexes from 000001 NOT duplicated:
--   idx_user_sessions_user_revoked  (user_id, revoked_at)
--   idx_user_sessions_user_device   (user_id, device_id)
--   idx_user_devices_user_revoked   (user_id, revoked_at)
--
-- New indexes add the sort column and a partial device index with revoked_at.

BEGIN;

SET LOCAL search_path = auth, public;

-- List sessions newest-first per user (ORDER BY created_at DESC, id DESC).
CREATE INDEX idx_user_sessions_user_created
    ON auth.user_sessions (user_id, created_at DESC, id DESC);

-- Device revocation cascade: find active sessions by (user, device_id) with revoked_at filter.
-- Partial: excludes rows with NULL device_id (sessions not bound to any device).
-- Different from existing idx_user_sessions_user_device (user_id, device_id) — adds revoked_at.
CREATE INDEX idx_user_sessions_user_device_revoked
    ON auth.user_sessions (user_id, device_id, revoked_at)
    WHERE device_id IS NOT NULL;

-- List devices newest-first per user (ORDER BY last_seen_at DESC, id DESC).
CREATE INDEX idx_user_devices_user_last_seen
    ON auth.user_devices (user_id, last_seen_at DESC, id DESC);

COMMIT;
```

- [ ] **Step 2: Create the down migration**

```sql
-- migrations/auth/000006_device_session_indexes.down.sql
-- Drops only the indexes added in 000006. Does not touch extensions.

BEGIN;

SET LOCAL search_path = auth, public;

DROP INDEX IF EXISTS auth.idx_user_sessions_user_created;
DROP INDEX IF EXISTS auth.idx_user_sessions_user_device_revoked;
DROP INDEX IF EXISTS auth.idx_user_devices_user_last_seen;

COMMIT;
```

- [ ] **Step 3: Validate migration syntax**

```bash
bash -n scripts/db/migrate.sh 2>/dev/null || true
pnpm migrations:check
```

Expected: passes syntax checks (no SQL execution — no live DB needed).

- [ ] **Step 4: Commit**

```bash
git add migrations/auth/000006_device_session_indexes.up.sql \
        migrations/auth/000006_device_session_indexes.down.sql
git commit -m "chore(db): add compound indexes for device/session management

- idx_user_sessions_user_created: list sessions ordered by recency
- idx_user_sessions_user_device_revoked: device cascade revocation (partial)
- idx_user_devices_user_last_seen: list devices ordered by recency

RF-51 RF-52 RF-53

Co-authored-by: Copilot <223556219+Copilot@users.noreply.github.com>"
```

---

## Task 2: Domain — New Types and ErrNotFound

**Files:**

- Modify: `services/auth-service/internal/domain/errors.go`
- Modify: `services/auth-service/internal/domain/auth.go`

- [ ] **Step 1: Add ErrNotFound to errors.go**

Open `services/auth-service/internal/domain/errors.go` and append after the last `var`:

```go
var ErrNotFound = errors.New("not found")
```

The full file becomes:

```go
package domain

import "errors"

var (
	ErrDuplicateEmail         = errors.New("email already registered")
	ErrPasswordPolicy         = errors.New("password does not meet policy requirements")
	ErrInvalidInput           = errors.New("invalid input")
	ErrInvalidToken           = errors.New("invalid or expired token")
	ErrEmailOutboxUnavailable = errors.New("email outbox encryption unavailable")
)

var ErrInvalidRefreshToken = errors.New("invalid refresh token")

var ErrInvalidCredentials = errors.New("invalid credentials")

var ErrInviteAlreadyPending = errors.New("active invite already exists for this email")

var ErrNotFound = errors.New("not found")
```

- [ ] **Step 2: Add domain types to auth.go**

Append to the end of `services/auth-service/internal/domain/auth.go`:

```go
// SessionInfo is the safe, displayable representation of a user session row.
// IPAddress and UserAgent are raw — masking/sanitizing is done in the HTTP layer.
type SessionInfo struct {
	ID                string
	DeviceID          *string
	CreatedAt         time.Time
	LastSeenAt        time.Time
	IdleExpiresAt     time.Time
	AbsoluteExpiresAt *time.Time
	RevokedAt         *time.Time
	IPAddress         string
	UserAgent         string
}

// DeviceInfo is the safe, displayable representation of a user device row.
// LastIP is raw — masking is done in the HTTP layer.
// Current is true when the current access token's session belongs to this device.
type DeviceInfo struct {
	ID           string
	DisplayName  *string
	Platform     *string
	LastIP       string
	FirstSeenAt  time.Time
	LastSeenAt   time.Time
	RevokedAt    *time.Time
	SessionCount int
	Current      bool
}

// DeviceSessionPolicy carries policy fields surfaced by the device/session endpoints.
type DeviceSessionPolicy struct {
	MaxDevicesPerUser int
}
```

- [ ] **Step 3: Compile**

```bash
cd services/auth-service && go build ./...
```

Expected: compiles with no errors.

- [ ] **Step 4: Commit**

```bash
git add services/auth-service/internal/domain/errors.go \
        services/auth-service/internal/domain/auth.go
git commit -m "feat(auth): add session/device domain types and ErrNotFound

SessionInfo, DeviceInfo, DeviceSessionPolicy for RF-51/RF-52/RF-53 endpoints.
ErrNotFound sentinel for cross-user 404 handling.

Co-authored-by: Copilot <223556219+Copilot@users.noreply.github.com>"
```

---

## Task 3: BearerAuth — Inject ctxKeySessionID

**Files:**

- Modify: `services/auth-service/internal/http/bearer_middleware.go`
- Modify: `services/auth-service/internal/http/bearer_middleware_test.go`

- [ ] **Step 1: Write the failing test for session ID injection**

Add to `services/auth-service/internal/http/bearer_middleware_test.go` (inside `package httpapi_test`):

```go
func TestBearerAuth_ValidJWT_InjectsSessionID(t *testing.T) {
	tokens := makeTestTokenManager(t)
	accessToken, _, err := tokens.GenerateAccessToken("user-abc", "session-xyz")
	if err != nil {
		t.Fatalf("generate access token: %v", err)
	}

	var capturedSessionID string
	captureHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedSessionID = httpapi.GetContextSessionID(r)
		w.WriteHeader(http.StatusOK)
	})

	handler := httpapi.BearerAuth(tokens)(captureHandler)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if capturedSessionID != "session-xyz" {
		t.Fatalf("expected session ID 'session-xyz', got %q", capturedSessionID)
	}
}
```

- [ ] **Step 2: Run the test — verify it fails**

```bash
cd services/auth-service && go test ./internal/http/ -run TestBearerAuth_ValidJWT_InjectsSessionID -v
```

Expected: compilation error — `httpapi.GetContextSessionID` undefined.

- [ ] **Step 3: Update bearer_middleware.go**

Replace the full content of `services/auth-service/internal/http/bearer_middleware.go`:

```go
package httpapi

import (
	"context"
	"net/http"
	"strings"

	"github.com/nicrepository/nchat/libs/go/platform/httputil"
	"github.com/nicrepository/nchat/services/auth-service/internal/service"
)

// ctxKeyUserID is the context key for the authenticated userID.
type ctxKey int

const (
	ctxKeyUserID    ctxKey = iota
	ctxKeySessionID        // carries AccessClaims.SessionID ("sid"); "" if absent
)

// GetContextSessionID returns the session ID injected by BearerAuth, or "" if absent.
func GetContextSessionID(r *http.Request) string {
	sid, _ := r.Context().Value(ctxKeySessionID).(string)
	return sid
}

// BearerAuth extracts and validates a Bearer JWT access token.
// On success it injects the userID and sessionID into the request context and calls next.
// On failure it returns a generic auth error without leaking token details.
func BearerAuth(tokens *service.TokenManager) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if tokens == nil {
				httputil.WriteError(w, http.StatusServiceUnavailable, "service_unavailable", "auth disabled")
				return
			}

			hdr := r.Header.Get("Authorization")
			if !strings.HasPrefix(hdr, "Bearer ") {
				httputil.WriteError(w, http.StatusUnauthorized, httputil.ErrCodeUnauthorized, "unauthorized")
				return
			}

			raw := strings.TrimPrefix(hdr, "Bearer ")
			if raw == "" {
				httputil.WriteError(w, http.StatusUnauthorized, httputil.ErrCodeUnauthorized, "unauthorized")
				return
			}

			claims, err := tokens.ValidateAccessToken(raw)
			if err != nil {
				httputil.WriteError(w, http.StatusUnauthorized, httputil.ErrCodeUnauthorized, "unauthorized")
				return
			}

			ctx := context.WithValue(r.Context(), ctxKeyUserID, claims.Subject)
			ctx = context.WithValue(ctx, ctxKeySessionID, claims.SessionID)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
```

- [ ] **Step 4: Run test — verify it passes**

```bash
cd services/auth-service && go test ./internal/http/ -run TestBearerAuth -v
```

Expected: all `TestBearerAuth_*` tests PASS.

- [ ] **Step 5: Commit**

```bash
git add services/auth-service/internal/http/bearer_middleware.go \
        services/auth-service/internal/http/bearer_middleware_test.go
git commit -m "feat(auth): inject ctxKeySessionID from JWT sid claim in BearerAuth

Adds GetContextSessionID(r) helper for handlers to determine current session.
If sid claim is absent, value is '' — handlers treat current=false for all entries.

Co-authored-by: Copilot <223556219+Copilot@users.noreply.github.com>"
```

---

## Task 4: Storage — PGXDeviceSessionStore

**Files:**

- Create: `services/auth-service/internal/storage/device_session_store.go`
- Create: `services/auth-service/internal/storage/device_session_store_test.go`

- [ ] **Step 1: Write the failing tests**

Create `services/auth-service/internal/storage/device_session_store_test.go`:

```go
package storage_test

import (
	"context"
	"errors"
	"testing"
	"time"

	pgxmock "github.com/pashagolub/pgxmock/v2"

	"github.com/nicrepository/nchat/services/auth-service/internal/domain"
	"github.com/nicrepository/nchat/services/auth-service/internal/storage"
)

// ---- helpers ----------------------------------------------------------------

func newMockPool(t *testing.T) pgxmock.PgxPoolIface {
	t.Helper()
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	return mock
}

// ---- ListSessions -----------------------------------------------------------

func TestPGXDeviceSessionStore_ListSessions_ReturnsMappedRows(t *testing.T) {
	mock := newMockPool(t)
	defer mock.Close()

	now := time.Now().UTC().Truncate(time.Second)
	deviceID := "device-uuid-1"

	mock.ExpectQuery(`SELECT id, device_id`).
		WithArgs("user-1", false, 50).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "device_id", "created_at", "last_seen_at",
			"idle_expires_at", "absolute_expires_at", "revoked_at",
			"ip_address", "user_agent",
		}).AddRow(
			"session-1", &deviceID, now, now,
			now.Add(time.Hour), nil, nil,
			"192.168.1.1", "Mozilla/5.0",
		))

	store := storage.NewPGXDeviceSessionStore(mock)
	sessions, err := store.ListSessions(context.Background(), "user-1", false, 50)
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("expected 1 session, got %d", len(sessions))
	}
	if sessions[0].ID != "session-1" {
		t.Fatalf("expected ID 'session-1', got %q", sessions[0].ID)
	}
	if sessions[0].DeviceID == nil || *sessions[0].DeviceID != "device-uuid-1" {
		t.Fatalf("expected DeviceID 'device-uuid-1', got %v", sessions[0].DeviceID)
	}
	if sessions[0].IPAddress != "192.168.1.1" {
		t.Fatalf("expected IPAddress '192.168.1.1', got %q", sessions[0].IPAddress)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXDeviceSessionStore_ListSessions_IncludeRevoked(t *testing.T) {
	mock := newMockPool(t)
	defer mock.Close()

	mock.ExpectQuery(`SELECT id, device_id`).
		WithArgs("user-1", true, 10).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "device_id", "created_at", "last_seen_at",
			"idle_expires_at", "absolute_expires_at", "revoked_at",
			"ip_address", "user_agent",
		}))

	store := storage.NewPGXDeviceSessionStore(mock)
	_, err := store.ListSessions(context.Background(), "user-1", true, 10)
	if err != nil {
		t.Fatalf("ListSessions include_revoked: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// ---- RevokeSession ----------------------------------------------------------

func TestPGXDeviceSessionStore_RevokeSession_RevokesSessionAndHistory(t *testing.T) {
	mock := newMockPool(t)
	defer mock.Close()

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT id FROM auth\.user_sessions`).
		WithArgs("session-1", "user-1").
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow("session-1"))
	mock.ExpectExec(`UPDATE auth\.user_sessions`).
		WithArgs("session-1", "user-1").
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectExec(`UPDATE auth\.refresh_token_history`).
		WithArgs("session-1").
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectCommit()
	mock.ExpectRollback()

	store := storage.NewPGXDeviceSessionStore(mock)
	if err := store.RevokeSession(context.Background(), "session-1", "user-1"); err != nil {
		t.Fatalf("RevokeSession: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXDeviceSessionStore_RevokeSession_NotFound_ReturnsErrNotFound(t *testing.T) {
	mock := newMockPool(t)
	defer mock.Close()

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT id FROM auth\.user_sessions`).
		WithArgs("no-such-session", "user-1").
		WillReturnError(pgxmock.ErrCancelled)
	mock.ExpectRollback()

	store := storage.NewPGXDeviceSessionStore(mock)
	err := store.RevokeSession(context.Background(), "no-such-session", "user-1")
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

// ---- RevokeAllSessionsExcept ------------------------------------------------

func TestPGXDeviceSessionStore_RevokeAllSessionsExcept_RunsCTE(t *testing.T) {
	mock := newMockPool(t)
	defer mock.Close()

	mock.ExpectBegin()
	mock.ExpectExec(`WITH revoked AS`).
		WithArgs("user-1", "current-session").
		WillReturnResult(pgxmock.NewResult("UPDATE", 2))
	mock.ExpectCommit()
	mock.ExpectRollback()

	store := storage.NewPGXDeviceSessionStore(mock)
	if err := store.RevokeAllSessionsExcept(context.Background(), "user-1", "current-session"); err != nil {
		t.Fatalf("RevokeAllSessionsExcept: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// ---- ListDevices ------------------------------------------------------------

func TestPGXDeviceSessionStore_ListDevices_ReturnsMappedRows(t *testing.T) {
	mock := newMockPool(t)
	defer mock.Close()

	now := time.Now().UTC().Truncate(time.Second)

	mock.ExpectQuery(`SELECT d\.id`).
		WithArgs("user-1", "", false, 50).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "display_name", "platform", "last_ip",
			"first_seen_at", "last_seen_at", "revoked_at",
			"session_count", "current",
		}).AddRow(
			"device-1", nil, nil, "10.0.0.1",
			now, now, nil,
			2, false,
		))
	mock.ExpectQuery(`SELECT max_devices_per_user`).
		WillReturnRows(pgxmock.NewRows([]string{"max_devices_per_user"}).AddRow(5))

	store := storage.NewPGXDeviceSessionStore(mock)
	devices, policy, err := store.ListDevices(context.Background(), "user-1", "", false, 50)
	if err != nil {
		t.Fatalf("ListDevices: %v", err)
	}
	if len(devices) != 1 {
		t.Fatalf("expected 1 device, got %d", len(devices))
	}
	if devices[0].ID != "device-1" {
		t.Fatalf("expected ID 'device-1', got %q", devices[0].ID)
	}
	if devices[0].SessionCount != 2 {
		t.Fatalf("expected SessionCount 2, got %d", devices[0].SessionCount)
	}
	if policy.MaxDevicesPerUser != 5 {
		t.Fatalf("expected MaxDevicesPerUser 5, got %d", policy.MaxDevicesPerUser)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXDeviceSessionStore_ListDevices_EmptySessionID_NeverCastsUUID(t *testing.T) {
	mock := newMockPool(t)
	defer mock.Close()

	now := time.Now().UTC().Truncate(time.Second)

	mock.ExpectQuery(`SELECT d\.id`).
		WithArgs("user-1", "", false, 50).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "display_name", "platform", "last_ip",
			"first_seen_at", "last_seen_at", "revoked_at",
			"session_count", "current",
		}).AddRow("device-2", nil, nil, "", now, now, nil, 0, false))
	mock.ExpectQuery(`SELECT max_devices_per_user`).
		WillReturnRows(pgxmock.NewRows([]string{"max_devices_per_user"}).AddRow(5))

	store := storage.NewPGXDeviceSessionStore(mock)
	devices, _, err := store.ListDevices(context.Background(), "user-1", "", false, 50)
	if err != nil {
		t.Fatalf("ListDevices empty sid: %v", err)
	}
	if devices[0].Current {
		t.Fatal("expected current=false when session ID is empty")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// ---- RevokeDevice -----------------------------------------------------------

func TestPGXDeviceSessionStore_RevokeDevice_UsesCTE(t *testing.T) {
	mock := newMockPool(t)
	defer mock.Close()

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT id FROM auth\.user_devices`).
		WithArgs("device-1", "user-1").
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow("device-1"))
	mock.ExpectExec(`WITH revoked_device AS`).
		WithArgs("device-1", "user-1").
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectCommit()
	mock.ExpectRollback()

	store := storage.NewPGXDeviceSessionStore(mock)
	if err := store.RevokeDevice(context.Background(), "device-1", "user-1"); err != nil {
		t.Fatalf("RevokeDevice: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXDeviceSessionStore_RevokeDevice_NotFound_ReturnsErrNotFound(t *testing.T) {
	mock := newMockPool(t)
	defer mock.Close()

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT id FROM auth\.user_devices`).
		WithArgs("no-device", "user-1").
		WillReturnError(pgxmock.ErrCancelled)
	mock.ExpectRollback()

	store := storage.NewPGXDeviceSessionStore(mock)
	err := store.RevokeDevice(context.Background(), "no-device", "user-1")
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

// ---- UpdateDeviceDisplayName ------------------------------------------------

func TestPGXDeviceSessionStore_UpdateDeviceDisplayName_UpdatesActiveDevice(t *testing.T) {
	mock := newMockPool(t)
	defer mock.Close()

	mock.ExpectExec(`UPDATE auth\.user_devices SET display_name`).
		WithArgs("My Phone", "device-1", "user-1").
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	store := storage.NewPGXDeviceSessionStore(mock)
	if err := store.UpdateDeviceDisplayName(context.Background(), "device-1", "user-1", "My Phone"); err != nil {
		t.Fatalf("UpdateDeviceDisplayName: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXDeviceSessionStore_UpdateDeviceDisplayName_RevokedOrCrossUser_ReturnsErrNotFound(t *testing.T) {
	mock := newMockPool(t)
	defer mock.Close()

	mock.ExpectExec(`UPDATE auth\.user_devices SET display_name`).
		WithArgs("name", "device-x", "user-1").
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))

	store := storage.NewPGXDeviceSessionStore(mock)
	err := store.UpdateDeviceDisplayName(context.Background(), "device-x", "user-1", "name")
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}
```

- [ ] **Step 2: Run tests — verify they fail**

```bash
cd services/auth-service && go test ./internal/storage/ -run TestPGXDeviceSession -v 2>&1 | head -20
```

Expected: compile error — `storage.NewPGXDeviceSessionStore` undefined.

- [ ] **Step 3: Implement PGXDeviceSessionStore**

Create `services/auth-service/internal/storage/device_session_store.go`:

```go
package storage

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/nicrepository/nchat/services/auth-service/internal/domain"
)

// PGXDeviceSessionStore implements device and session management persistence.
type PGXDeviceSessionStore struct {
	pool Pool
}

// NewPGXDeviceSessionStore creates a PGXDeviceSessionStore backed by the given pool.
func NewPGXDeviceSessionStore(pool Pool) *PGXDeviceSessionStore {
	return &PGXDeviceSessionStore{pool: pool}
}

// ListSessions returns sessions for userID ordered newest first.
// includeRevoked=false returns only active sessions; true returns all.
func (s *PGXDeviceSessionStore) ListSessions(ctx context.Context, userID string, includeRevoked bool, limit int) ([]domain.SessionInfo, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, device_id, created_at, last_seen_at,
		       idle_expires_at, absolute_expires_at, revoked_at,
		       ip_address::text, user_agent
		FROM auth.user_sessions
		WHERE user_id = $1
		  AND ($2 OR revoked_at IS NULL)
		ORDER BY created_at DESC, id DESC
		LIMIT $3`,
		userID, includeRevoked, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	defer rows.Close()

	var sessions []domain.SessionInfo
	for rows.Next() {
		var si domain.SessionInfo
		var ip, ua *string
		if err := rows.Scan(
			&si.ID, &si.DeviceID, &si.CreatedAt, &si.LastSeenAt,
			&si.IdleExpiresAt, &si.AbsoluteExpiresAt, &si.RevokedAt,
			&ip, &ua,
		); err != nil {
			return nil, fmt.Errorf("scan session: %w", err)
		}
		if ip != nil {
			si.IPAddress = *ip
		}
		if ua != nil {
			si.UserAgent = *ua
		}
		sessions = append(sessions, si)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate sessions: %w", err)
	}
	return sessions, nil
}

// RevokeSession revokes the session identified by sessionID for userID.
// Returns ErrNotFound if the session does not exist or belongs to a different user.
// Idempotent: already-revoked own session returns nil.
func (s *PGXDeviceSessionStore) RevokeSession(ctx context.Context, sessionID, userID string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var id string
	if err := tx.QueryRow(ctx, `
		SELECT id FROM auth.user_sessions
		WHERE id = $1 AND user_id = $2
		FOR UPDATE`,
		sessionID, userID,
	).Scan(&id); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ErrNotFound
		}
		return fmt.Errorf("lock session: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		UPDATE auth.user_sessions
		SET revoked_at = now(), revoked_reason = 'user_revoked'
		WHERE id = $1 AND user_id = $2 AND revoked_at IS NULL`,
		sessionID, userID,
	); err != nil {
		return fmt.Errorf("revoke session: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		UPDATE auth.refresh_token_history
		SET status = 'revoked', revoked_at = now()
		WHERE session_id = $1 AND status = 'active'`,
		sessionID,
	); err != nil {
		return fmt.Errorf("revoke refresh token history: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit tx: %w", err)
	}
	return nil
}

// RevokeAllSessionsExcept revokes all sessions for userID except exceptSessionID,
// and their active refresh token history. Uses a CTE to avoid collecting IDs separately.
func (s *PGXDeviceSessionStore) RevokeAllSessionsExcept(ctx context.Context, userID, exceptSessionID string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	if _, err := tx.Exec(ctx, `
		WITH revoked AS (
		    UPDATE auth.user_sessions
		    SET revoked_at = now(), revoked_reason = 'user_revoked_all'
		    WHERE user_id = $1
		      AND id <> $2
		      AND revoked_at IS NULL
		    RETURNING id
		)
		UPDATE auth.refresh_token_history
		SET status = 'revoked', revoked_at = now()
		WHERE session_id IN (SELECT id FROM revoked)
		  AND status = 'active'`,
		userID, exceptSessionID,
	); err != nil {
		return fmt.Errorf("revoke all sessions: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit tx: %w", err)
	}
	return nil
}

// ListDevices returns devices for userID ordered by last_seen newest first.
// currentSessionID is used to mark one device as current; pass "" to skip.
// includeRevoked=false returns only active devices; true returns all.
func (s *PGXDeviceSessionStore) ListDevices(ctx context.Context, userID, currentSessionID string, includeRevoked bool, limit int) ([]domain.DeviceInfo, domain.DeviceSessionPolicy, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT d.id, d.display_name, d.platform,
		       d.last_ip::text, d.first_seen_at, d.last_seen_at, d.revoked_at,
		       COUNT(s.id) FILTER (WHERE s.revoked_at IS NULL) AS session_count,
		       CASE WHEN $2 <> '' THEN
		           d.id = (SELECT device_id FROM auth.user_sessions
		                   WHERE id = $2::uuid AND user_id = $1)
		       ELSE false END AS current
		FROM auth.user_devices AS d
		LEFT JOIN auth.user_sessions AS s ON s.device_id = d.id AND s.user_id = d.user_id
		WHERE d.user_id = $1
		  AND ($3 OR d.revoked_at IS NULL)
		GROUP BY d.id
		ORDER BY d.last_seen_at DESC, d.id DESC
		LIMIT $4`,
		userID, currentSessionID, includeRevoked, limit,
	)
	if err != nil {
		return nil, domain.DeviceSessionPolicy{}, fmt.Errorf("list devices: %w", err)
	}
	defer rows.Close()

	var devices []domain.DeviceInfo
	for rows.Next() {
		var di domain.DeviceInfo
		var lastIP *string
		var current *bool
		if err := rows.Scan(
			&di.ID, &di.DisplayName, &di.Platform,
			&lastIP, &di.FirstSeenAt, &di.LastSeenAt, &di.RevokedAt,
			&di.SessionCount, &current,
		); err != nil {
			return nil, domain.DeviceSessionPolicy{}, fmt.Errorf("scan device: %w", err)
		}
		if lastIP != nil {
			di.LastIP = *lastIP
		}
		if current != nil {
			di.Current = *current
		}
		devices = append(devices, di)
	}
	if err := rows.Err(); err != nil {
		return nil, domain.DeviceSessionPolicy{}, fmt.Errorf("iterate devices: %w", err)
	}

	var policy domain.DeviceSessionPolicy
	if err := s.pool.QueryRow(ctx, `
		SELECT max_devices_per_user FROM auth.auth_policy_settings WHERE id = 1`,
	).Scan(&policy.MaxDevicesPerUser); err != nil {
		return nil, domain.DeviceSessionPolicy{}, fmt.Errorf("get device policy: %w", err)
	}

	return devices, policy, nil
}

// RevokeDevice revokes the device identified by deviceID for userID, along with
// all active sessions and their refresh token history, using a single CTE.
// Returns ErrNotFound if the device does not exist or belongs to a different user.
// Idempotent: already-revoked own device returns nil.
func (s *PGXDeviceSessionStore) RevokeDevice(ctx context.Context, deviceID, userID string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var id string
	if err := tx.QueryRow(ctx, `
		SELECT id FROM auth.user_devices
		WHERE id = $1 AND user_id = $2
		FOR UPDATE`,
		deviceID, userID,
	).Scan(&id); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ErrNotFound
		}
		return fmt.Errorf("lock device: %w", err)
	}

	if _, err := tx.Exec(ctx, `
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
		  AND status = 'active'`,
		deviceID, userID,
	); err != nil {
		return fmt.Errorf("revoke device: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit tx: %w", err)
	}
	return nil
}

// UpdateDeviceDisplayName sets the display_name of an active device.
// Returns ErrNotFound if the device does not exist, is revoked, or belongs to a different user.
func (s *PGXDeviceSessionStore) UpdateDeviceDisplayName(ctx context.Context, deviceID, userID, name string) error {
	result, err := s.pool.Exec(ctx, `
		UPDATE auth.user_devices
		SET display_name = $1
		WHERE id = $2 AND user_id = $3 AND revoked_at IS NULL`,
		nullableString(name), deviceID, userID,
	)
	if err != nil {
		return fmt.Errorf("update device display name: %w", err)
	}
	if result.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}
```

- [ ] **Step 4: Run tests — verify they pass**

```bash
cd services/auth-service && go test ./internal/storage/ -run TestPGXDeviceSession -v
```

Expected: all `TestPGXDeviceSessionStore_*` tests PASS.

- [ ] **Step 5: Commit**

```bash
git add services/auth-service/internal/storage/device_session_store.go \
        services/auth-service/internal/storage/device_session_store_test.go
git commit -m "feat(auth): add PGXDeviceSessionStore for session/device management

Implements ListSessions, RevokeSession, RevokeAllSessionsExcept,
ListDevices, RevokeDevice, UpdateDeviceDisplayName.
RevokeDevice uses CTE to cascade revocation without collecting IDs.

Co-authored-by: Copilot <223556219+Copilot@users.noreply.github.com>"
```

---

## Task 5: Service — DeviceSessionService

**Files:**

- Create: `services/auth-service/internal/service/device_session_service.go`
- Create: `services/auth-service/internal/service/device_session_service_test.go`

- [ ] **Step 1: Write the failing tests**

Create `services/auth-service/internal/service/device_session_service_test.go`:

```go
package service_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/nicrepository/nchat/services/auth-service/internal/domain"
	"github.com/nicrepository/nchat/services/auth-service/internal/service"
)

// fakeDeviceSessionStore is a minimal fake for DeviceSessionStore.
type fakeDeviceSessionStore struct {
	listSessionsErr     error
	revokeSessionErr    error
	revokeAllErr        error
	listDevicesErr      error
	revokeDeviceErr     error
	updateDisplayErr    error

	lastSessionIDRevoked string
	lastExceptSessionID  string
	lastDeviceIDRevoked  string
	lastDisplayName      string
	lastIncludeRevoked   bool
	lastLimit            int
}

func (f *fakeDeviceSessionStore) ListSessions(_ context.Context, _ string, includeRevoked bool, limit int) ([]domain.SessionInfo, error) {
	f.lastIncludeRevoked = includeRevoked
	f.lastLimit = limit
	return nil, f.listSessionsErr
}
func (f *fakeDeviceSessionStore) RevokeSession(_ context.Context, sessionID, _ string) error {
	f.lastSessionIDRevoked = sessionID
	return f.revokeSessionErr
}
func (f *fakeDeviceSessionStore) RevokeAllSessionsExcept(_ context.Context, _, exceptSessionID string) error {
	f.lastExceptSessionID = exceptSessionID
	return f.revokeAllErr
}
func (f *fakeDeviceSessionStore) ListDevices(_ context.Context, _ string, _ string, includeRevoked bool, limit int) ([]domain.DeviceInfo, domain.DeviceSessionPolicy, error) {
	f.lastIncludeRevoked = includeRevoked
	f.lastLimit = limit
	return nil, domain.DeviceSessionPolicy{}, f.listDevicesErr
}
func (f *fakeDeviceSessionStore) RevokeDevice(_ context.Context, deviceID, _ string) error {
	f.lastDeviceIDRevoked = deviceID
	return f.revokeDeviceErr
}
func (f *fakeDeviceSessionStore) UpdateDeviceDisplayName(_ context.Context, _, _, name string) error {
	f.lastDisplayName = name
	return f.updateDisplayErr
}

// ---- limit clamping ---------------------------------------------------------

func TestDeviceSessionService_ListSessions_ClampsLimit(t *testing.T) {
	store := &fakeDeviceSessionStore{}
	svc := service.NewDeviceSessionService(store)

	_, _ = svc.ListSessions(context.Background(), "user-1", false, 200)
	if store.lastLimit != 100 {
		t.Fatalf("expected limit clamped to 100, got %d", store.lastLimit)
	}
}

func TestDeviceSessionService_ListSessions_DefaultsLimit(t *testing.T) {
	store := &fakeDeviceSessionStore{}
	svc := service.NewDeviceSessionService(store)

	_, _ = svc.ListSessions(context.Background(), "user-1", false, 0)
	if store.lastLimit != 50 {
		t.Fatalf("expected default limit 50, got %d", store.lastLimit)
	}
}

func TestDeviceSessionService_ListDevices_ClampsLimit(t *testing.T) {
	store := &fakeDeviceSessionStore{}
	svc := service.NewDeviceSessionService(store)

	_, _, _ = svc.ListDevices(context.Background(), "user-1", "", false, 200)
	if store.lastLimit != 100 {
		t.Fatalf("expected limit clamped to 100, got %d", store.lastLimit)
	}
}

// ---- display name validation ------------------------------------------------

func TestDeviceSessionService_UpdateDisplayName_ValidatesMinLength(t *testing.T) {
	store := &fakeDeviceSessionStore{}
	svc := service.NewDeviceSessionService(store)

	err := svc.UpdateDeviceDisplayName(context.Background(), "device-1", "user-1", "")
	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput for empty name, got %v", err)
	}
}

func TestDeviceSessionService_UpdateDisplayName_ValidatesMaxLength(t *testing.T) {
	store := &fakeDeviceSessionStore{}
	svc := service.NewDeviceSessionService(store)

	longName := strings.Repeat("a", 81)
	err := svc.UpdateDeviceDisplayName(context.Background(), "device-1", "user-1", longName)
	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput for name >80 chars, got %v", err)
	}
}

func TestDeviceSessionService_UpdateDisplayName_StripsControlChars(t *testing.T) {
	store := &fakeDeviceSessionStore{}
	svc := service.NewDeviceSessionService(store)

	_ = svc.UpdateDeviceDisplayName(context.Background(), "device-1", "user-1", "My\x00Phone\r\n")
	if store.lastDisplayName != "MyPhone" {
		t.Fatalf("expected control chars stripped, got %q", store.lastDisplayName)
	}
}

func TestDeviceSessionService_UpdateDisplayName_TrimsWhitespace(t *testing.T) {
	store := &fakeDeviceSessionStore{}
	svc := service.NewDeviceSessionService(store)

	_ = svc.UpdateDeviceDisplayName(context.Background(), "device-1", "user-1", "  My Phone  ")
	if store.lastDisplayName != "My Phone" {
		t.Fatalf("expected trimmed name, got %q", store.lastDisplayName)
	}
}

func TestDeviceSessionService_UpdateDisplayName_ValidName_Delegates(t *testing.T) {
	store := &fakeDeviceSessionStore{}
	svc := service.NewDeviceSessionService(store)

	if err := svc.UpdateDeviceDisplayName(context.Background(), "device-1", "user-1", "My Phone"); err != nil {
		t.Fatalf("expected no error for valid name, got %v", err)
	}
	if store.lastDisplayName != "My Phone" {
		t.Fatalf("expected 'My Phone' passed to store, got %q", store.lastDisplayName)
	}
}

// ---- delegation -------------------------------------------------------------

func TestDeviceSessionService_RevokeSession_Delegates(t *testing.T) {
	store := &fakeDeviceSessionStore{}
	svc := service.NewDeviceSessionService(store)

	_ = svc.RevokeSession(context.Background(), "session-abc", "user-1")
	if store.lastSessionIDRevoked != "session-abc" {
		t.Fatalf("expected session-abc delegated, got %q", store.lastSessionIDRevoked)
	}
}

func TestDeviceSessionService_RevokeDevice_Delegates(t *testing.T) {
	store := &fakeDeviceSessionStore{}
	svc := service.NewDeviceSessionService(store)

	_ = svc.RevokeDevice(context.Background(), "device-abc", "user-1")
	if store.lastDeviceIDRevoked != "device-abc" {
		t.Fatalf("expected device-abc delegated, got %q", store.lastDeviceIDRevoked)
	}
}
```

- [ ] **Step 2: Run tests — verify they fail**

```bash
cd services/auth-service && go test ./internal/service/ -run TestDeviceSessionService -v 2>&1 | head -10
```

Expected: compile error — `service.NewDeviceSessionService` undefined.

- [ ] **Step 3: Implement DeviceSessionService**

Create `services/auth-service/internal/service/device_session_service.go`:

```go
package service

import (
	"context"
	"fmt"
	"strings"
	"unicode"

	"github.com/nicrepository/nchat/services/auth-service/internal/domain"
)

const (
	deviceDisplayNameMinLen = 1
	deviceDisplayNameMaxLen = 80
)

// DeviceSessionStore is the persistence interface for DeviceSessionService.
type DeviceSessionStore interface {
	ListSessions(ctx context.Context, userID string, includeRevoked bool, limit int) ([]domain.SessionInfo, error)
	RevokeSession(ctx context.Context, sessionID, userID string) error
	RevokeAllSessionsExcept(ctx context.Context, userID, exceptSessionID string) error
	ListDevices(ctx context.Context, userID, currentSessionID string, includeRevoked bool, limit int) ([]domain.DeviceInfo, domain.DeviceSessionPolicy, error)
	RevokeDevice(ctx context.Context, deviceID, userID string) error
	UpdateDeviceDisplayName(ctx context.Context, deviceID, userID, name string) error
}

// DeviceSessionManager is the HTTP-facing interface for device/session management.
type DeviceSessionManager interface {
	ListSessions(ctx context.Context, userID string, includeRevoked bool, limit int) ([]domain.SessionInfo, error)
	RevokeSession(ctx context.Context, sessionID, userID string) error
	RevokeAllSessionsExcept(ctx context.Context, userID, exceptSessionID string) error
	ListDevices(ctx context.Context, userID, currentSessionID string, includeRevoked bool, limit int) ([]domain.DeviceInfo, domain.DeviceSessionPolicy, error)
	RevokeDevice(ctx context.Context, deviceID, userID string) error
	UpdateDeviceDisplayName(ctx context.Context, deviceID, userID, name string) error
}

// DeviceSessionService implements DeviceSessionManager.
type DeviceSessionService struct {
	store DeviceSessionStore
}

// NewDeviceSessionService creates a DeviceSessionService backed by the given store.
func NewDeviceSessionService(store DeviceSessionStore) *DeviceSessionService {
	return &DeviceSessionService{store: store}
}

func (s *DeviceSessionService) ListSessions(ctx context.Context, userID string, includeRevoked bool, limit int) ([]domain.SessionInfo, error) {
	limit = clampDeviceSessionLimit(limit)
	return s.store.ListSessions(ctx, userID, includeRevoked, limit)
}

func (s *DeviceSessionService) RevokeSession(ctx context.Context, sessionID, userID string) error {
	return s.store.RevokeSession(ctx, sessionID, userID)
}

func (s *DeviceSessionService) RevokeAllSessionsExcept(ctx context.Context, userID, exceptSessionID string) error {
	return s.store.RevokeAllSessionsExcept(ctx, userID, exceptSessionID)
}

func (s *DeviceSessionService) ListDevices(ctx context.Context, userID, currentSessionID string, includeRevoked bool, limit int) ([]domain.DeviceInfo, domain.DeviceSessionPolicy, error) {
	limit = clampDeviceSessionLimit(limit)
	return s.store.ListDevices(ctx, userID, currentSessionID, includeRevoked, limit)
}

func (s *DeviceSessionService) RevokeDevice(ctx context.Context, deviceID, userID string) error {
	return s.store.RevokeDevice(ctx, deviceID, userID)
}

// UpdateDeviceDisplayName validates and sanitizes the display name, then delegates.
// Returns ErrInvalidInput if name (after trim and strip) is empty or exceeds 80 chars.
func (s *DeviceSessionService) UpdateDeviceDisplayName(ctx context.Context, deviceID, userID, name string) error {
	name = sanitizeDisplayName(name)
	if len([]rune(name)) < deviceDisplayNameMinLen {
		return fmt.Errorf("%w: display_name must be at least 1 character", domain.ErrInvalidInput)
	}
	if len([]rune(name)) > deviceDisplayNameMaxLen {
		return fmt.Errorf("%w: display_name must be at most 80 characters", domain.ErrInvalidInput)
	}
	return s.store.UpdateDeviceDisplayName(ctx, deviceID, userID, name)
}

// sanitizeDisplayName trims whitespace and removes control characters (NUL, CR, LF, etc.).
func sanitizeDisplayName(name string) string {
	name = strings.TrimSpace(name)
	var sb strings.Builder
	for _, r := range name {
		if unicode.IsControl(r) {
			continue
		}
		sb.WriteRune(r)
	}
	return sb.String()
}

// clampDeviceSessionLimit clamps limit to [1, 100]; 0 or negative defaults to 50.
func clampDeviceSessionLimit(limit int) int {
	if limit <= 0 {
		return 50
	}
	if limit > 100 {
		return 100
	}
	return limit
}
```

- [ ] **Step 4: Run tests — verify they pass**

```bash
cd services/auth-service && go test ./internal/service/ -run TestDeviceSessionService -v
```

Expected: all `TestDeviceSessionService_*` tests PASS.

- [ ] **Step 5: Commit**

```bash
git add services/auth-service/internal/service/device_session_service.go \
        services/auth-service/internal/service/device_session_service_test.go
git commit -m "feat(auth): add DeviceSessionService with display_name validation

Interfaces: DeviceSessionStore (storage), DeviceSessionManager (HTTP).
Validates display_name: trim, strip controls, 1-80 chars.
Clamps list limits to [1,100]; default 50.

Co-authored-by: Copilot <223556219+Copilot@users.noreply.github.com>"
```

---

## Task 6: HTTP — Session Handlers

**Files:**

- Create: `services/auth-service/internal/http/session_handler.go`
- Create: `services/auth-service/internal/http/session_handler_test.go`

- [ ] **Step 1: Write the failing tests**

Create `services/auth-service/internal/http/session_handler_test.go`:

```go
package httpapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/nicrepository/nchat/services/auth-service/internal/domain"
	httpapi "github.com/nicrepository/nchat/services/auth-service/internal/http"
)

// ---- mock -------------------------------------------------------------------

type mockSessionManager struct {
	sessions    []domain.SessionInfo
	listErr     error
	revokeErr   error
	revokeAllErr error

	lastUserID        string
	lastSessionID     string
	lastExceptSession string
	lastIncludeRevoked bool
	lastLimit         int
}

func (m *mockSessionManager) ListSessions(_ context.Context, userID string, includeRevoked bool, limit int) ([]domain.SessionInfo, error) {
	m.lastUserID = userID
	m.lastIncludeRevoked = includeRevoked
	m.lastLimit = limit
	return m.sessions, m.listErr
}
func (m *mockSessionManager) RevokeSession(_ context.Context, sessionID, userID string) error {
	m.lastSessionID = sessionID
	m.lastUserID = userID
	return m.revokeErr
}
func (m *mockSessionManager) RevokeAllSessionsExcept(_ context.Context, userID, exceptSessionID string) error {
	m.lastUserID = userID
	m.lastExceptSession = exceptSessionID
	return m.revokeAllErr
}

// ---- GET /auth/me/sessions --------------------------------------------------

func TestGetMySessions_NoBearer_Returns401(t *testing.T) {
	tokens := makeTestTokenManager(t)
	handler := httpapi.BearerAuth(tokens)(httpapi.GetMySessions(&mockSessionManager{}))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/auth/me/sessions", nil)
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
	assertErrorCode(t, rec.Body.Bytes(), "unauthorized")
}

func TestGetMySessions_ReturnsOnlyOwnSessions(t *testing.T) {
	tokens := makeTestTokenManager(t)
	userID := "user-sess-1"
	accessToken, _, err := tokens.GenerateAccessToken(userID, "session-curr")
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}

	now := time.Now().UTC()
	svc := &mockSessionManager{
		sessions: []domain.SessionInfo{
			{ID: "session-curr", CreatedAt: now, LastSeenAt: now, IdleExpiresAt: now.Add(time.Hour)},
			{ID: "session-old", CreatedAt: now.Add(-time.Hour), LastSeenAt: now, IdleExpiresAt: now.Add(time.Hour)},
		},
	}

	handler := httpapi.BearerAuth(tokens)(httpapi.GetMySessions(svc))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/auth/me/sessions", nil)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if svc.lastUserID != userID {
		t.Fatalf("expected userID %q passed to service, got %q", userID, svc.lastUserID)
	}

	var envelope struct {
		Data struct {
			Data []struct {
				ID      string `json:"id"`
				Current bool   `json:"current"`
			} `json:"data"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(envelope.Data.Data) != 2 {
		t.Fatalf("expected 2 sessions, got %d", len(envelope.Data.Data))
	}
	// session-curr is the current session (matches JWT sid claim)
	var foundCurrent bool
	for _, s := range envelope.Data.Data {
		if s.ID == "session-curr" && s.Current {
			foundCurrent = true
		}
	}
	if !foundCurrent {
		t.Fatal("expected session-curr to have current=true")
	}
}

func TestGetMySessions_HidesSensitiveFields(t *testing.T) {
	tokens := makeTestTokenManager(t)
	accessToken, _, err := tokens.GenerateAccessToken("user-1", "sid-1")
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}

	now := time.Now().UTC()
	svc := &mockSessionManager{
		sessions: []domain.SessionInfo{
			{ID: "sid-1", IPAddress: "1.2.3.4", UserAgent: "Mozilla/5.0",
				CreatedAt: now, LastSeenAt: now, IdleExpiresAt: now.Add(time.Hour)},
		},
	}

	handler := httpapi.BearerAuth(tokens)(httpapi.GetMySessions(svc))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/auth/me/sessions", nil)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	handler.ServeHTTP(rec, req)

	body := rec.Body.String()
	if containsAny(body, "refresh_token_hash", "device_fingerprint_hash", "password") {
		t.Fatalf("response must not contain sensitive fields: %s", body)
	}
	// IP must be masked
	if containsAny(body, "1.2.3.4") {
		t.Fatalf("response must not contain raw IP: %s", body)
	}
	if !containsAny(body, "1.2.*.*") {
		t.Fatalf("expected masked IP '1.2.*.*' in response: %s", body)
	}
}

func TestGetMySessions_IncludeRevokedQueryParam(t *testing.T) {
	tokens := makeTestTokenManager(t)
	accessToken, _, err := tokens.GenerateAccessToken("user-1", "sid-1")
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}

	svc := &mockSessionManager{}
	handler := httpapi.BearerAuth(tokens)(httpapi.GetMySessions(svc))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/auth/me/sessions?include_revoked=true", nil)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if !svc.lastIncludeRevoked {
		t.Fatal("expected include_revoked=true passed to service")
	}
}

// ---- DELETE /auth/me/sessions/{session_id} ----------------------------------

func TestDeleteMySession_NoBearer_Returns401(t *testing.T) {
	tokens := makeTestTokenManager(t)
	handler := httpapi.BearerAuth(tokens)(httpapi.DeleteMySession(&mockSessionManager{}))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/auth/me/sessions/some-id", nil)
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestDeleteMySession_InvalidUUID_Returns400(t *testing.T) {
	tokens := makeTestTokenManager(t)
	accessToken, _, err := tokens.GenerateAccessToken("user-1", "sid-1")
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}

	req := httptest.NewRequest(http.MethodDelete, "/auth/me/sessions/not-a-uuid", nil)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.SetPathValue("session_id", "not-a-uuid")

	handler := httpapi.BearerAuth(tokens)(httpapi.DeleteMySession(&mockSessionManager{}))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
	assertErrorCode(t, rec.Body.Bytes(), "bad_request")
}

func TestDeleteMySession_OwnSession_Returns204(t *testing.T) {
	tokens := makeTestTokenManager(t)
	userID := "user-del-1"
	accessToken, _, err := tokens.GenerateAccessToken(userID, "sid-curr")
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}

	validSessionID := "123e4567-e89b-12d3-a456-426614174000"
	svc := &mockSessionManager{}

	req := httptest.NewRequest(http.MethodDelete, "/auth/me/sessions/"+validSessionID, nil)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.SetPathValue("session_id", validSessionID)

	handler := httpapi.BearerAuth(tokens)(httpapi.DeleteMySession(svc))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", rec.Code, rec.Body.String())
	}
	if svc.lastUserID != userID {
		t.Fatalf("expected userID %q, got %q", userID, svc.lastUserID)
	}
}

func TestDeleteMySession_CrossUserSession_Returns404(t *testing.T) {
	tokens := makeTestTokenManager(t)
	accessToken, _, err := tokens.GenerateAccessToken("user-1", "sid-1")
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}

	validSessionID := "123e4567-e89b-12d3-a456-426614174001"
	svc := &mockSessionManager{revokeErr: domain.ErrNotFound}

	req := httptest.NewRequest(http.MethodDelete, "/auth/me/sessions/"+validSessionID, nil)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.SetPathValue("session_id", validSessionID)

	handler := httpapi.BearerAuth(tokens)(httpapi.DeleteMySession(svc))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
	assertErrorCode(t, rec.Body.Bytes(), "not_found")
}

// ---- DELETE /auth/me/sessions (bulk) ----------------------------------------

func TestDeleteAllMySessions_NoSidInToken_Returns409(t *testing.T) {
	// Generate a token without sid by using an empty session ID.
	// TokenManager.GenerateAccessToken requires non-empty sessionID, so we create
	// a token with an empty string session ID via the service layer workaround:
	// We use a real token but then manually test what happens when sid="" in context.
	tokens := makeTestTokenManager(t)
	// Generate valid token with a real sid
	accessToken, _, err := tokens.GenerateAccessToken("user-1", "sid-real")
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}

	svc := &mockSessionManager{}
	handler := httpapi.BearerAuth(tokens)(httpapi.DeleteAllMySessions(svc))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/auth/me/sessions", nil)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	// Inject empty session ID to simulate absent sid
	ctx := req.Context()
	req = req.WithContext(httpapi.WithContextSessionIDForTest(ctx, ""))
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409 when sid absent, got %d", rec.Code)
	}
	assertErrorCode(t, rec.Body.Bytes(), "conflict")
}

func TestDeleteAllMySessions_ValidToken_Returns204(t *testing.T) {
	tokens := makeTestTokenManager(t)
	userID := "user-del-all"
	currentSID := "sid-current-123"
	accessToken, _, err := tokens.GenerateAccessToken(userID, currentSID)
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}

	svc := &mockSessionManager{}
	handler := httpapi.BearerAuth(tokens)(httpapi.DeleteAllMySessions(svc))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/auth/me/sessions", nil)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", rec.Code, rec.Body.String())
	}
	if svc.lastExceptSession != currentSID {
		t.Fatalf("expected exceptSession %q, got %q", currentSID, svc.lastExceptSession)
	}
}
```

**Note on `WithContextSessionIDForTest`:** This is a test-only exported helper that lets us inject a custom session ID into context, bypassing BearerAuth. Add it to `bearer_middleware.go` (or a new `testhelpers_test.go` internal test file) — see Step 3 below.

- [ ] **Step 2: Run tests — verify they fail**

```bash
cd services/auth-service && go test ./internal/http/ -run TestGetMySessions -v 2>&1 | head -20
```

Expected: compile error — `httpapi.GetMySessions` undefined.

- [ ] **Step 3: Create session_handler.go**

Create `services/auth-service/internal/http/session_handler.go`:

```go
package httpapi

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"time"

	"github.com/nicrepository/nchat/libs/go/platform/httputil"
	"github.com/nicrepository/nchat/services/auth-service/internal/domain"
)

var uuidRE = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func isValidUUID(s string) bool { return uuidRE.MatchString(s) }

// SessionManager is the service interface for session management endpoints.
type SessionManager interface {
	ListSessions(ctx context.Context, userID string, includeRevoked bool, limit int) ([]domain.SessionInfo, error)
	RevokeSession(ctx context.Context, sessionID, userID string) error
	RevokeAllSessionsExcept(ctx context.Context, userID, exceptSessionID string) error
}

type sessionResponse struct {
	ID                string  `json:"id"`
	DeviceID          *string `json:"device_id"`
	CreatedAt         string  `json:"created_at"`
	LastSeenAt        string  `json:"last_seen_at"`
	IdleExpiresAt     string  `json:"idle_expires_at"`
	AbsoluteExpiresAt *string `json:"absolute_expires_at"`
	RevokedAt         *string `json:"revoked_at"`
	IPAddress         string  `json:"ip_address,omitempty"`
	UserAgent         string  `json:"user_agent,omitempty"`
	Current           bool    `json:"current"`
}

type sessionsListResponse struct {
	Data       []sessionResponse       `json:"data"`
	Pagination loginAttemptsPagination `json:"pagination"`
}

// GetMySessions returns the authenticated user's sessions, newest first.
func GetMySessions(svc SessionManager) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if svc == nil {
			httputil.WriteError(w, http.StatusServiceUnavailable, "service_unavailable", "sessions endpoint disabled")
			return
		}

		userID, ok := r.Context().Value(ctxKeyUserID).(string)
		if !ok || userID == "" {
			httputil.WriteError(w, http.StatusUnauthorized, httputil.ErrCodeUnauthorized, "unauthorized")
			return
		}
		currentSessionID := GetContextSessionID(r)

		limit := 50
		if s := r.URL.Query().Get("limit"); s != "" {
			if n, err := strconv.Atoi(s); err == nil && n > 0 {
				limit = n
			}
		}

		includeRevoked := r.URL.Query().Get("include_revoked") == "true"

		sessions, err := svc.ListSessions(r.Context(), userID, includeRevoked, limit)
		if err != nil {
			httputil.WriteError(w, http.StatusInternalServerError, httputil.ErrCodeInternal, "internal error")
			return
		}

		data := make([]sessionResponse, 0, len(sessions))
		for _, s := range sessions {
			resp := sessionResponse{
				ID:            s.ID,
				DeviceID:      s.DeviceID,
				CreatedAt:     s.CreatedAt.Format(time.RFC3339),
				LastSeenAt:    s.LastSeenAt.Format(time.RFC3339),
				IdleExpiresAt: s.IdleExpiresAt.Format(time.RFC3339),
				IPAddress:     maskIPAddress(s.IPAddress),
				UserAgent:     sanitizeUserAgent(s.UserAgent),
				Current:       currentSessionID != "" && s.ID == currentSessionID,
			}
			if s.AbsoluteExpiresAt != nil {
				t := s.AbsoluteExpiresAt.Format(time.RFC3339)
				resp.AbsoluteExpiresAt = &t
			}
			if s.RevokedAt != nil {
				t := s.RevokedAt.Format(time.RFC3339)
				resp.RevokedAt = &t
			}
			data = append(data, resp)
		}

		httputil.WriteJSON(w, http.StatusOK, sessionsListResponse{
			Data:       data,
			Pagination: loginAttemptsPagination{Limit: limit},
		})
	})
}

// DeleteMySession revokes a single session owned by the authenticated user.
// Returns 204 on success or already-revoked own session.
// Returns 404 for unknown or cross-user session.
// Returns 400 for malformed session_id.
func DeleteMySession(svc SessionManager) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if svc == nil {
			httputil.WriteError(w, http.StatusServiceUnavailable, "service_unavailable", "sessions endpoint disabled")
			return
		}

		userID, ok := r.Context().Value(ctxKeyUserID).(string)
		if !ok || userID == "" {
			httputil.WriteError(w, http.StatusUnauthorized, httputil.ErrCodeUnauthorized, "unauthorized")
			return
		}

		sessionID := r.PathValue("session_id")
		if !isValidUUID(sessionID) {
			httputil.WriteError(w, http.StatusBadRequest, httputil.ErrCodeBadRequest, "invalid session_id")
			return
		}

		if err := svc.RevokeSession(r.Context(), sessionID, userID); err != nil {
			if errors.Is(err, domain.ErrNotFound) {
				httputil.WriteError(w, http.StatusNotFound, httputil.ErrCodeNotFound, "session not found")
				return
			}
			httputil.WriteError(w, http.StatusInternalServerError, httputil.ErrCodeInternal, "internal error")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

// DeleteAllMySessions revokes all sessions for the authenticated user except the current one.
// Returns 409 if the access token does not carry a session ID (sid claim absent).
func DeleteAllMySessions(svc SessionManager) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if svc == nil {
			httputil.WriteError(w, http.StatusServiceUnavailable, "service_unavailable", "sessions endpoint disabled")
			return
		}

		userID, ok := r.Context().Value(ctxKeyUserID).(string)
		if !ok || userID == "" {
			httputil.WriteError(w, http.StatusUnauthorized, httputil.ErrCodeUnauthorized, "unauthorized")
			return
		}

		currentSessionID := GetContextSessionID(r)
		if currentSessionID == "" {
			httputil.WriteError(w, http.StatusConflict, "conflict", "cannot determine current session; access token missing sid claim")
			return
		}

		if err := svc.RevokeAllSessionsExcept(r.Context(), userID, currentSessionID); err != nil {
			httputil.WriteError(w, http.StatusInternalServerError, httputil.ErrCodeInternal, "internal error")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}
```

- [ ] **Step 4: Add internal test for 409 (absent session ID)**

The 409 case requires injecting `ctxKeyUserID` without `ctxKeySessionID` into the request context — not possible from `package httpapi_test` without a helper. Place it in an **internal** test file (`package httpapi`) that has access to unexported context keys.

Add to `services/auth-service/internal/http/test_fixtures_internal_test.go` (already exists in `package httpapi`):

```go
import (
    "context"
    "net/http"
    "net/http/httptest"
    "testing"
)

func TestDeleteAllMySessions_NoSidInContext_Returns409(t *testing.T) {
    svc := &fakeSessionManager{}
    handler := DeleteAllMySessions(svc)

    rec := httptest.NewRecorder()
    req := httptest.NewRequest("DELETE", "/auth/me/sessions", nil)
    // Inject userID but no sessionID (ctxKeySessionID defaults to "")
    ctx := context.WithValue(req.Context(), ctxKeyUserID, "user-nosid")
    req = req.WithContext(ctx)
    handler.ServeHTTP(rec, req)

    if rec.Code != 409 {
        t.Fatalf("expected 409 when sid absent, got %d: %s", rec.Code, rec.Body.String())
    }
}

// fakeSessionManager is a minimal stub for internal tests.
type fakeSessionManager struct {
    revokeAllErr error
    lastExcept   string
}

func (f *fakeSessionManager) ListSessions(_ context.Context, _ string, _ bool, _ int) ([]domain.SessionInfo, error) {
    return nil, nil
}
func (f *fakeSessionManager) RevokeSession(_ context.Context, _, _ string) error { return nil }
func (f *fakeSessionManager) RevokeAllSessionsExcept(_ context.Context, _, exceptSID string) error {
    f.lastExcept = exceptSID
    return f.revokeAllErr
}
```

Note: `domain` import is needed in the internal test file. Add the import alongside existing ones.

- [ ] **Step 5: Run all session handler tests — verify they pass**

```bash
cd services/auth-service && go test ./internal/http/ -run "TestGetMySessions|TestDeleteMySession|TestDeleteAllMySessions" -v
```

Expected: all tests PASS.

- [ ] **Step 6: Commit**

```bash
git add services/auth-service/internal/http/session_handler.go \
        services/auth-service/internal/http/session_handler_test.go \
        services/auth-service/internal/http/test_fixtures_internal_test.go
git commit -m "feat(auth): add session management HTTP handlers

GET /auth/me/sessions — list own sessions with current marker, IP masking, UA sanitizing.
DELETE /auth/me/sessions/{session_id} — revoke own session; 404 for cross-user; UUID validation.
DELETE /auth/me/sessions — revoke all except current; 409 if sid absent from token.

Co-authored-by: Copilot <223556219+Copilot@users.noreply.github.com>"
```

---

## Task 7: HTTP — Device Handlers

**Files:**

- Create: `services/auth-service/internal/http/device_handler.go`
- Create: `services/auth-service/internal/http/device_handler_test.go`

- [ ] **Step 1: Write the failing tests**

Create `services/auth-service/internal/http/device_handler_test.go`:

```go
package httpapi_test

import (
"context"
"encoding/json"
"errors"
"net/http"
"net/http/httptest"
"strings"
"testing"
"time"

"github.com/nicrepository/nchat/services/auth-service/internal/domain"
httpapi "github.com/nicrepository/nchat/services/auth-service/internal/http"
)

// ---- mock -------------------------------------------------------------------

type mockDeviceManager struct {
devices       []domain.DeviceInfo
policy        domain.DeviceSessionPolicy
listErr       error
revokeErr     error
updateNameErr error

lastUserID      string
lastDeviceID    string
lastDisplayName string
lastIncludeRevoked bool
}

func (m *mockDeviceManager) ListDevices(_ context.Context, userID, _ string, includeRevoked bool, _ int) ([]domain.DeviceInfo, domain.DeviceSessionPolicy, error) {
m.lastUserID = userID
m.lastIncludeRevoked = includeRevoked
return m.devices, m.policy, m.listErr
}
func (m *mockDeviceManager) RevokeDevice(_ context.Context, deviceID, userID string) error {
m.lastDeviceID = deviceID
m.lastUserID = userID
return m.revokeErr
}
func (m *mockDeviceManager) UpdateDeviceDisplayName(_ context.Context, deviceID, userID, name string) error {
m.lastDeviceID = deviceID
m.lastUserID = userID
m.lastDisplayName = name
return m.updateNameErr
}

// ---- GET /auth/me/devices ---------------------------------------------------

func TestGetMyDevices_NoBearer_Returns401(t *testing.T) {
tokens := makeTestTokenManager(t)
handler := httpapi.BearerAuth(tokens)(httpapi.GetMyDevices(&mockDeviceManager{}))
rec := httptest.NewRecorder()
req := httptest.NewRequest(http.MethodGet, "/auth/me/devices", nil)
handler.ServeHTTP(rec, req)
if rec.Code != http.StatusUnauthorized {
t.Fatalf("expected 401, got %d", rec.Code)
}
assertErrorCode(t, rec.Body.Bytes(), "unauthorized")
}

func TestGetMyDevices_ReturnsOnlyOwnDevices(t *testing.T) {
tokens := makeTestTokenManager(t)
userID := "user-dev-1"
accessToken, _, err := tokens.GenerateAccessToken(userID, "session-d1")
if err != nil {
t.Fatalf("generate token: %v", err)
}

now := time.Now().UTC()
svc := &mockDeviceManager{
devices: []domain.DeviceInfo{
{ID: "device-1", LastSeenAt: now, FirstSeenAt: now, SessionCount: 1, Current: true},
},
policy: domain.DeviceSessionPolicy{MaxDevicesPerUser: 5},
}

handler := httpapi.BearerAuth(tokens)(httpapi.GetMyDevices(svc))
rec := httptest.NewRecorder()
req := httptest.NewRequest(http.MethodGet, "/auth/me/devices", nil)
req.Header.Set("Authorization", "Bearer "+accessToken)
handler.ServeHTTP(rec, req)

if rec.Code != http.StatusOK {
t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
}
if svc.lastUserID != userID {
t.Fatalf("expected userID %q, got %q", userID, svc.lastUserID)
}

var envelope struct {
Data struct {
Data []struct {
ID           string `json:"id"`
SessionCount int    `json:"session_count"`
Current      bool   `json:"current"`
} `json:"data"`
Meta struct {
MaxDevicesPerUser int `json:"max_devices_per_user"`
} `json:"meta"`
} `json:"data"`
}
if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
t.Fatalf("decode: %v", err)
}
if len(envelope.Data.Data) != 1 {
t.Fatalf("expected 1 device, got %d", len(envelope.Data.Data))
}
if envelope.Data.Data[0].ID != "device-1" {
t.Fatalf("unexpected device id: %q", envelope.Data.Data[0].ID)
}
if envelope.Data.Data[0].SessionCount != 1 {
t.Fatalf("expected session_count 1, got %d", envelope.Data.Data[0].SessionCount)
}
if envelope.Data.Meta.MaxDevicesPerUser != 5 {
t.Fatalf("expected max_devices_per_user 5, got %d", envelope.Data.Meta.MaxDevicesPerUser)
}
}

func TestGetMyDevices_HidesSensitiveFields(t *testing.T) {
tokens := makeTestTokenManager(t)
accessToken, _, err := tokens.GenerateAccessToken("user-1", "sid-1")
if err != nil {
t.Fatalf("generate token: %v", err)
}

now := time.Now().UTC()
svc := &mockDeviceManager{
devices: []domain.DeviceInfo{
{ID: "d1", LastIP: "10.0.0.1", FirstSeenAt: now, LastSeenAt: now},
},
}

handler := httpapi.BearerAuth(tokens)(httpapi.GetMyDevices(svc))
rec := httptest.NewRecorder()
req := httptest.NewRequest(http.MethodGet, "/auth/me/devices", nil)
req.Header.Set("Authorization", "Bearer "+accessToken)
handler.ServeHTTP(rec, req)

body := rec.Body.String()
if containsAny(body, "device_fingerprint_hash", "refresh_token_hash", "password") {
t.Fatalf("response must not contain sensitive fields: %s", body)
}
// IP masked: 10.0.0.1 → 10.0.*.*
if containsAny(body, "10.0.0.1") {
t.Fatalf("raw IP must not appear in response: %s", body)
}
if !containsAny(body, "10.0.*.*") {
t.Fatalf("expected masked IP '10.0.*.*' in response: %s", body)
}
}

func TestGetMyDevices_IncludeRevokedQueryParam(t *testing.T) {
tokens := makeTestTokenManager(t)
accessToken, _, err := tokens.GenerateAccessToken("user-1", "sid-1")
if err != nil {
t.Fatalf("generate token: %v", err)
}

svc := &mockDeviceManager{}
handler := httpapi.BearerAuth(tokens)(httpapi.GetMyDevices(svc))
rec := httptest.NewRecorder()
req := httptest.NewRequest(http.MethodGet, "/auth/me/devices?include_revoked=true", nil)
req.Header.Set("Authorization", "Bearer "+accessToken)
handler.ServeHTTP(rec, req)

if rec.Code != http.StatusOK {
t.Fatalf("expected 200, got %d", rec.Code)
}
if !svc.lastIncludeRevoked {
t.Fatal("expected include_revoked=true passed to service")
}
}

// ---- DELETE /auth/me/devices/{device_id} ------------------------------------

func TestDeleteMyDevice_NoBearer_Returns401(t *testing.T) {
tokens := makeTestTokenManager(t)
handler := httpapi.BearerAuth(tokens)(httpapi.DeleteMyDevice(&mockDeviceManager{}))
rec := httptest.NewRecorder()
req := httptest.NewRequest(http.MethodDelete, "/auth/me/devices/some-id", nil)
handler.ServeHTTP(rec, req)
if rec.Code != http.StatusUnauthorized {
t.Fatalf("expected 401, got %d", rec.Code)
}
}

func TestDeleteMyDevice_InvalidUUID_Returns400(t *testing.T) {
tokens := makeTestTokenManager(t)
accessToken, _, err := tokens.GenerateAccessToken("user-1", "sid-1")
if err != nil {
t.Fatalf("generate token: %v", err)
}

req := httptest.NewRequest(http.MethodDelete, "/auth/me/devices/not-a-uuid", nil)
req.Header.Set("Authorization", "Bearer "+accessToken)
req.SetPathValue("device_id", "not-a-uuid")

handler := httpapi.BearerAuth(tokens)(httpapi.DeleteMyDevice(&mockDeviceManager{}))
rec := httptest.NewRecorder()
handler.ServeHTTP(rec, req)
if rec.Code != http.StatusBadRequest {
t.Fatalf("expected 400, got %d", rec.Code)
}
assertErrorCode(t, rec.Body.Bytes(), "bad_request")
}

func TestDeleteMyDevice_OwnDevice_Returns204(t *testing.T) {
tokens := makeTestTokenManager(t)
userID := "user-revoke-dev"
accessToken, _, err := tokens.GenerateAccessToken(userID, "sid-curr")
if err != nil {
t.Fatalf("generate token: %v", err)
}

validDeviceID := "123e4567-e89b-12d3-a456-426614174002"
svc := &mockDeviceManager{}

req := httptest.NewRequest(http.MethodDelete, "/auth/me/devices/"+validDeviceID, nil)
req.Header.Set("Authorization", "Bearer "+accessToken)
req.SetPathValue("device_id", validDeviceID)

handler := httpapi.BearerAuth(tokens)(httpapi.DeleteMyDevice(svc))
rec := httptest.NewRecorder()
handler.ServeHTTP(rec, req)

if rec.Code != http.StatusNoContent {
t.Fatalf("expected 204, got %d: %s", rec.Code, rec.Body.String())
}
if svc.lastUserID != userID {
t.Fatalf("expected userID %q, got %q", userID, svc.lastUserID)
}
}

func TestDeleteMyDevice_CrossUserDevice_Returns404(t *testing.T) {
tokens := makeTestTokenManager(t)
accessToken, _, err := tokens.GenerateAccessToken("user-1", "sid-1")
if err != nil {
t.Fatalf("generate token: %v", err)
}

validDeviceID := "123e4567-e89b-12d3-a456-426614174003"
svc := &mockDeviceManager{revokeErr: domain.ErrNotFound}

req := httptest.NewRequest(http.MethodDelete, "/auth/me/devices/"+validDeviceID, nil)
req.Header.Set("Authorization", "Bearer "+accessToken)
req.SetPathValue("device_id", validDeviceID)

handler := httpapi.BearerAuth(tokens)(httpapi.DeleteMyDevice(svc))
rec := httptest.NewRecorder()
handler.ServeHTTP(rec, req)

if rec.Code != http.StatusNotFound {
t.Fatalf("expected 404, got %d", rec.Code)
}
assertErrorCode(t, rec.Body.Bytes(), "not_found")
}

// ---- PATCH /auth/me/devices/{device_id} -------------------------------------

func TestPatchMyDevice_NoBearer_Returns401(t *testing.T) {
tokens := makeTestTokenManager(t)
handler := httpapi.BearerAuth(tokens)(httpapi.PatchMyDevice(&mockDeviceManager{}))
rec := httptest.NewRecorder()
req := httptest.NewRequest(http.MethodPatch, "/auth/me/devices/some-id", nil)
handler.ServeHTTP(rec, req)
if rec.Code != http.StatusUnauthorized {
t.Fatalf("expected 401, got %d", rec.Code)
}
}

func TestPatchMyDevice_InvalidUUID_Returns400(t *testing.T) {
tokens := makeTestTokenManager(t)
accessToken, _, err := tokens.GenerateAccessToken("user-1", "sid-1")
if err != nil {
t.Fatalf("generate token: %v", err)
}

req := httptest.NewRequest(http.MethodPatch, "/auth/me/devices/bad-id",
strings.NewReader(`{"display_name":"My Phone"}`))
req.Header.Set("Authorization", "Bearer "+accessToken)
req.Header.Set("Content-Type", "application/json")
req.SetPathValue("device_id", "bad-id")

handler := httpapi.BearerAuth(tokens)(httpapi.PatchMyDevice(&mockDeviceManager{}))
rec := httptest.NewRecorder()
handler.ServeHTTP(rec, req)
if rec.Code != http.StatusBadRequest {
t.Fatalf("expected 400, got %d", rec.Code)
}
}

func TestPatchMyDevice_ValidName_Returns204(t *testing.T) {
tokens := makeTestTokenManager(t)
userID := "user-patch-dev"
accessToken, _, err := tokens.GenerateAccessToken(userID, "sid-curr")
if err != nil {
t.Fatalf("generate token: %v", err)
}

validDeviceID := "123e4567-e89b-12d3-a456-426614174004"
svc := &mockDeviceManager{}

req := httptest.NewRequest(http.MethodPatch, "/auth/me/devices/"+validDeviceID,
strings.NewReader(`{"display_name":"My Laptop"}`))
req.Header.Set("Authorization", "Bearer "+accessToken)
req.Header.Set("Content-Type", "application/json")
req.SetPathValue("device_id", validDeviceID)

handler := httpapi.BearerAuth(tokens)(httpapi.PatchMyDevice(svc))
rec := httptest.NewRecorder()
handler.ServeHTTP(rec, req)

if rec.Code != http.StatusNoContent {
t.Fatalf("expected 204, got %d: %s", rec.Code, rec.Body.String())
}
if svc.lastDisplayName != "My Laptop" {
t.Fatalf("expected display_name 'My Laptop', got %q", svc.lastDisplayName)
}
if svc.lastUserID != userID {
t.Fatalf("expected userID %q, got %q", userID, svc.lastUserID)
}
}

func TestPatchMyDevice_RevokedOrCrossUser_Returns404(t *testing.T) {
tokens := makeTestTokenManager(t)
accessToken, _, err := tokens.GenerateAccessToken("user-1", "sid-1")
if err != nil {
t.Fatalf("generate token: %v", err)
}

validDeviceID := "123e4567-e89b-12d3-a456-426614174005"
svc := &mockDeviceManager{updateNameErr: domain.ErrNotFound}

req := httptest.NewRequest(http.MethodPatch, "/auth/me/devices/"+validDeviceID,
strings.NewReader(`{"display_name":"Test"}`))
req.Header.Set("Authorization", "Bearer "+accessToken)
req.Header.Set("Content-Type", "application/json")
req.SetPathValue("device_id", validDeviceID)

handler := httpapi.BearerAuth(tokens)(httpapi.PatchMyDevice(svc))
rec := httptest.NewRecorder()
handler.ServeHTTP(rec, req)

if rec.Code != http.StatusNotFound {
t.Fatalf("expected 404, got %d", rec.Code)
}
}

func TestPatchMyDevice_InvalidDisplayName_Returns400(t *testing.T) {
tokens := makeTestTokenManager(t)
accessToken, _, err := tokens.GenerateAccessToken("user-1", "sid-1")
if err != nil {
t.Fatalf("generate token: %v", err)
}

validDeviceID := "123e4567-e89b-12d3-a456-426614174006"
svc := &mockDeviceManager{updateNameErr: domain.ErrInvalidInput}

req := httptest.NewRequest(http.MethodPatch, "/auth/me/devices/"+validDeviceID,
strings.NewReader(`{"display_name":""}`))
req.Header.Set("Authorization", "Bearer "+accessToken)
req.Header.Set("Content-Type", "application/json")
req.SetPathValue("device_id", validDeviceID)

handler := httpapi.BearerAuth(tokens)(httpapi.PatchMyDevice(svc))
rec := httptest.NewRecorder()
handler.ServeHTTP(rec, req)

if rec.Code != http.StatusBadRequest {
t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
}
assertErrorCode(t, rec.Body.Bytes(), "bad_request")
}
```

- [ ] **Step 2: Run tests — verify they fail**

```bash
cd services/auth-service && go test ./internal/http/ -run "TestGetMyDevices|TestDeleteMyDevice|TestPatchMyDevice" -v 2>&1 | head -10
```

Expected: compile error — `httpapi.GetMyDevices` undefined.

- [ ] **Step 3: Create device_handler.go**

Create `services/auth-service/internal/http/device_handler.go`:

```go
package httpapi

import (
"context"
"encoding/json"
"errors"
"net/http"
"time"

"github.com/nicrepository/nchat/libs/go/platform/httputil"
"github.com/nicrepository/nchat/services/auth-service/internal/domain"
)

// DeviceManager is the service interface for device management endpoints.
type DeviceManager interface {
ListDevices(ctx context.Context, userID, currentSessionID string, includeRevoked bool, limit int) ([]domain.DeviceInfo, domain.DeviceSessionPolicy, error)
RevokeDevice(ctx context.Context, deviceID, userID string) error
UpdateDeviceDisplayName(ctx context.Context, deviceID, userID, name string) error
}

type deviceResponse struct {
ID          string  `json:"id"`
DisplayName *string `json:"display_name"`
Platform    *string `json:"platform"`
LastIP      string  `json:"last_ip,omitempty"`
FirstSeenAt string  `json:"first_seen_at"`
LastSeenAt  string  `json:"last_seen_at"`
RevokedAt   *string `json:"revoked_at"`
SessionCount int    `json:"session_count"`
Current     bool    `json:"current"`
}

type devicesMeta struct {
MaxDevicesPerUser int `json:"max_devices_per_user"`
}

type devicesListResponse struct {
Data       []deviceResponse        `json:"data"`
Meta       devicesMeta             `json:"meta"`
Pagination loginAttemptsPagination `json:"pagination"`
}

type patchDeviceRequest struct {
DisplayName string `json:"display_name"`
}

// GetMyDevices returns the authenticated user's linked devices.
func GetMyDevices(svc DeviceManager) http.Handler {
return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
if svc == nil {
httputil.WriteError(w, http.StatusServiceUnavailable, "service_unavailable", "devices endpoint disabled")
return
}

userID, ok := r.Context().Value(ctxKeyUserID).(string)
if !ok || userID == "" {
httputil.WriteError(w, http.StatusUnauthorized, httputil.ErrCodeUnauthorized, "unauthorized")
return
}
currentSessionID := GetContextSessionID(r)

limit := 50
includeRevoked := r.URL.Query().Get("include_revoked") == "true"

devices, policy, err := svc.ListDevices(r.Context(), userID, currentSessionID, includeRevoked, limit)
if err != nil {
httputil.WriteError(w, http.StatusInternalServerError, httputil.ErrCodeInternal, "internal error")
return
}

data := make([]deviceResponse, 0, len(devices))
for _, d := range devices {
resp := deviceResponse{
ID:           d.ID,
DisplayName:  d.DisplayName,
Platform:     d.Platform,
LastIP:       maskIPAddress(d.LastIP),
FirstSeenAt:  d.FirstSeenAt.Format(time.RFC3339),
LastSeenAt:   d.LastSeenAt.Format(time.RFC3339),
SessionCount: d.SessionCount,
Current:      d.Current,
}
if d.RevokedAt != nil {
t := d.RevokedAt.Format(time.RFC3339)
resp.RevokedAt = &t
}
data = append(data, resp)
}

httputil.WriteJSON(w, http.StatusOK, devicesListResponse{
Data:       data,
Meta:       devicesMeta{MaxDevicesPerUser: policy.MaxDevicesPerUser},
Pagination: loginAttemptsPagination{Limit: limit},
})
})
}

// DeleteMyDevice revokes a device owned by the authenticated user and all its sessions.
// Returns 204 on success or already-revoked own device.
// Returns 404 for unknown or cross-user device.
// Returns 400 for malformed device_id.
func DeleteMyDevice(svc DeviceManager) http.Handler {
return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
if svc == nil {
httputil.WriteError(w, http.StatusServiceUnavailable, "service_unavailable", "devices endpoint disabled")
return
}

userID, ok := r.Context().Value(ctxKeyUserID).(string)
if !ok || userID == "" {
httputil.WriteError(w, http.StatusUnauthorized, httputil.ErrCodeUnauthorized, "unauthorized")
return
}

deviceID := r.PathValue("device_id")
if !isValidUUID(deviceID) {
httputil.WriteError(w, http.StatusBadRequest, httputil.ErrCodeBadRequest, "invalid device_id")
return
}

if err := svc.RevokeDevice(r.Context(), deviceID, userID); err != nil {
if errors.Is(err, domain.ErrNotFound) {
httputil.WriteError(w, http.StatusNotFound, httputil.ErrCodeNotFound, "device not found")
return
}
httputil.WriteError(w, http.StatusInternalServerError, httputil.ErrCodeInternal, "internal error")
return
}
w.WriteHeader(http.StatusNoContent)
})
}

// PatchMyDevice updates the display_name of an active device owned by the authenticated user.
// Returns 204 on success.
// Returns 404 for unknown, revoked, or cross-user device.
// Returns 400 for malformed device_id or invalid display_name.
func PatchMyDevice(svc DeviceManager) http.Handler {
return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
if svc == nil {
httputil.WriteError(w, http.StatusServiceUnavailable, "service_unavailable", "devices endpoint disabled")
return
}

userID, ok := r.Context().Value(ctxKeyUserID).(string)
if !ok || userID == "" {
httputil.WriteError(w, http.StatusUnauthorized, httputil.ErrCodeUnauthorized, "unauthorized")
return
}

deviceID := r.PathValue("device_id")
if !isValidUUID(deviceID) {
httputil.WriteError(w, http.StatusBadRequest, httputil.ErrCodeBadRequest, "invalid device_id")
return
}

var body patchDeviceRequest
if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
httputil.WriteError(w, http.StatusBadRequest, httputil.ErrCodeBadRequest, "invalid request body")
return
}

if err := svc.UpdateDeviceDisplayName(r.Context(), deviceID, userID, body.DisplayName); err != nil {
switch {
case errors.Is(err, domain.ErrNotFound):
httputil.WriteError(w, http.StatusNotFound, httputil.ErrCodeNotFound, "device not found")
case errors.Is(err, domain.ErrInvalidInput):
httputil.WriteError(w, http.StatusBadRequest, httputil.ErrCodeBadRequest, err.Error())
default:
httputil.WriteError(w, http.StatusInternalServerError, httputil.ErrCodeInternal, "internal error")
}
return
}
w.WriteHeader(http.StatusNoContent)
})
}
```

- [ ] **Step 4: Run all device handler tests — verify they pass**

```bash
cd services/auth-service && go test ./internal/http/ -run "TestGetMyDevices|TestDeleteMyDevice|TestPatchMyDevice" -v
```

Expected: all tests PASS.

- [ ] **Step 5: Commit**

```bash
git add services/auth-service/internal/http/device_handler.go \
        services/auth-service/internal/http/device_handler_test.go
git commit -m "feat(auth): add device management HTTP handlers

GET /auth/me/devices — list own devices with session count, IP masking, max_devices_per_user.
DELETE /auth/me/devices/{device_id} — revoke device + sessions; 404 cross-user; UUID validation.
PATCH /auth/me/devices/{device_id} — update display_name; only active devices; 404 revoked.

Co-authored-by: Copilot <223556219+Copilot@users.noreply.github.com>"
```

---

## Task 8: Routes, Router, App Wiring

**Files:**

- Modify: `services/auth-service/internal/http/routes.go`
- Modify: `services/auth-service/internal/http/router.go`
- Modify: `services/auth-service/internal/http/router_test.go`
- Modify: `services/auth-service/internal/app/app.go`

- [ ] **Step 1: Add 6 route constants to routes.go**

Open `services/auth-service/internal/http/routes.go` and append after the last constant:

```go
const (
RouteHealthz             = "/healthz"
RouteReadyz              = "/readyz"
RouteVersion             = "/version"
RouteAdminUsers          = "/admin/users"
RouteAdminInvites        = "/admin/invites"
RouteAuthRefresh         = "/auth/refresh"
RouteAuthLogout          = "/auth/logout"
RouteAuthLogin           = "/auth/login"
RouteAuthPasswordForgot  = "/auth/password/forgot"
RouteAuthPasswordReset   = "/auth/password/reset"
RouteAuthInvitesAccept   = "/auth/invites/accept"
RouteAuthMeLoginAttempts = "/auth/me/login-attempts"
RouteAuthMeSessions      = "/auth/me/sessions"
RouteAuthMeSessionByID   = "/auth/me/sessions/{session_id}"
RouteAuthMeDevices       = "/auth/me/devices"
RouteAuthMeDeviceByID    = "/auth/me/devices/{device_id}"
)
```

Note: `RouteAuthMeSessions` covers both `GET` (list) and `DELETE` (bulk revoke) via `MethodNotAllowed`-style routing at the router level.

- [ ] **Step 2: Update NewRouter signature and handler registration**

Open `services/auth-service/internal/http/router.go`. Add two parameters and register the new handlers.

Replace the `func NewRouter(...)` signature and body:

```go
func NewRouter(cfg config.Config, logger *slog.Logger, users service.UserCreator, auth service.AuthSessionManager, login service.LoginManager, password service.PasswordRecoveryManager, invites service.InviteManager, loginAttempts LoginAttemptsManager, sessions SessionManager, devices DeviceManager) http.Handler {
```

Inside the function body, after the existing `mux.Handle(RouteAuthMeLoginAttempts, ...)` block, add:

```go
sessionsHandler := GetMySessions(sessions)
deleteSessionHandler := DeleteMySession(sessions)
deleteAllSessionsHandler := DeleteAllMySessions(sessions)
if tokens != nil && sessions != nil {
sessionsHandler = BearerAuth(tokens)(sessionsHandler)
deleteSessionHandler = BearerAuth(tokens)(deleteSessionHandler)
deleteAllSessionsHandler = BearerAuth(tokens)(deleteAllSessionsHandler)
}
mux.Handle("GET "+RouteAuthMeSessions, sessionsHandler)
mux.Handle("DELETE "+RouteAuthMeSessionByID, deleteSessionHandler)
mux.Handle("DELETE "+RouteAuthMeSessions, deleteAllSessionsHandler)

devicesHandler := GetMyDevices(devices)
deleteDeviceHandler := DeleteMyDevice(devices)
patchDeviceHandler := PatchMyDevice(devices)
if tokens != nil && devices != nil {
devicesHandler = BearerAuth(tokens)(devicesHandler)
deleteDeviceHandler = BearerAuth(tokens)(deleteDeviceHandler)
patchDeviceHandler = BearerAuth(tokens)(patchDeviceHandler)
}
mux.Handle("GET "+RouteAuthMeDevices, devicesHandler)
mux.Handle("DELETE "+RouteAuthMeDeviceByID, deleteDeviceHandler)
mux.Handle("PATCH "+RouteAuthMeDeviceByID, patchDeviceHandler)
```

Note: Go 1.22+ `net/http` mux supports `METHOD /path` patterns. The `{session_id}` and `{device_id}` patterns use the stdlib path params feature (`r.PathValue("session_id")`).

- [ ] **Step 3: Update router_test.go — add two nil args to every NewRouter call**

`router_test.go` calls `NewRouter(...)` ~15 times with 8 arguments. Each call must get two additional `nil` arguments. Run this sed command to do it automatically:

```bash
sed -i 's/NewRouter(testConfig(), platformlog.New("auth-service", "test"), nil, nil, nil, nil, nil, nil)/NewRouter(testConfig(), platformlog.New("auth-service", "test"), nil, nil, nil, nil, nil, nil, nil, nil)/g' \
    services/auth-service/internal/http/router_test.go

# Also fix the calls with non-nil arguments (routerAuthStub, routerLoginStub):
sed -i 's/NewRouter(cfg, platformlog.New("auth-service", "test"), nil, routerAuthStub{}, nil, nil, nil, nil)/NewRouter(cfg, platformlog.New("auth-service", "test"), nil, routerAuthStub{}, nil, nil, nil, nil, nil, nil)/g' \
    services/auth-service/internal/http/router_test.go

sed -i 's/NewRouter(cfg, platformlog.New("auth-service", "test"), nil, nil, routerLoginStub{}, nil, nil, nil)/NewRouter(cfg, platformlog.New("auth-service", "test"), nil, nil, routerLoginStub{}, nil, nil, nil, nil, nil)/g' \
    services/auth-service/internal/http/router_test.go
```

Verify all calls are updated:

```bash
grep -c "NewRouter(" services/auth-service/internal/http/router_test.go
grep "NewRouter(" services/auth-service/internal/http/router_test.go | grep -v "nil, nil, nil, nil, nil, nil, nil, nil"
```

Expected: second grep returns empty (all calls have 10 args including 2 new nils).

- [ ] **Step 4: Update app.go to wire DeviceSessionService**

Open `services/auth-service/internal/app/app.go`. Add the new service inside the `default:` case of the `switch`:

Change the `NewRouter` call and add the new service:

```go
var loginAttempts httpapi.LoginAttemptsManager
var deviceSessions service.DeviceSessionManager
switch {
case err != nil:
logger.Warn("auth token endpoints disabled", "reason", "invalid_jwt_config")
case pool == nil:
logger.Warn("auth token endpoints disabled", "reason", "database_not_configured")
default:
auth = service.NewAuthService(tokens, storage.NewPGXSessionStore(pool))
login = service.NewLoginService(tokens, storage.NewPGXLoginStore(pool, service.VerifyPassword, service.RunDummyPasswordVerification))
password = service.NewPasswordResetService(tokens, storage.NewPGXPasswordResetStore(pool), service.WithPasswordResetOutboxEncryptor(emailOutboxEncryptor))
invites = service.NewInviteService(tokens, storage.NewPGXInviteStore(pool), service.WithInviteOutboxEncryptor(emailOutboxEncryptor))
loginAttempts = service.NewLoginAttemptsService(storage.NewPGXLoginAttemptsStore(pool))
deviceSessions = service.NewDeviceSessionService(storage.NewPGXDeviceSessionStore(pool))
}

return &App{
Config:          cfg,
Logger:          logger,
Handler:         httpapi.NewRouter(cfg, logger, users, auth, login, password, invites, loginAttempts, deviceSessions, deviceSessions),
TracingShutdown: shutdown,
}
```

Note: `deviceSessions` implements both `SessionManager` and `DeviceManager` interfaces — it is passed twice for the two separate parameters.

- [ ] **Step 5: Build and run full test suite**

```bash
cd services/auth-service && go build ./... && go test -count=1 ./...
```

Expected: compiles and all tests pass.

- [ ] **Step 6: Commit**

```bash
git add services/auth-service/internal/http/routes.go \
        services/auth-service/internal/http/router.go \
        services/auth-service/internal/http/router_test.go \
        services/auth-service/internal/app/app.go
git commit -m "feat(auth): wire device/session handlers into router and app

Adds 6 route constants and registers GET/DELETE/PATCH handlers.
Uses method-qualified mux patterns (Go 1.22+) for precise routing.
DeviceSessionService satisfies both SessionManager and DeviceManager.

Co-authored-by: Copilot <223556219+Copilot@users.noreply.github.com>"
```

---

## Task 9: GitHub Issue, Runbook, README

**Files:**

- Create: `docs/runbooks/task-device-session-management.md`
- Modify: `README.md`

- [ ] **Step 1: Create GitHub issue**

```bash
gh issue create \
  --repo nicrepository/nchat \
  --title "feat(auth): add device and session management endpoints" \
  --body "## Summary

Implement authenticated REST endpoints in auth-service so users can view and manage their own sessions and linked devices.

## Requirements

- RF-51: session expires after inactivity (idle/absolute expiry surfaced in response)
- RF-52: multi-device limit configurable by admin; surfaced in device list response
- RF-53: linked device list visible and manageable by the user

## Endpoints

- \`GET /auth/me/sessions\` — list own sessions, active by default
- \`DELETE /auth/me/sessions/{session_id}\` — revoke own session
- \`DELETE /auth/me/sessions\` — revoke all except current
- \`GET /auth/me/devices\` — list own devices with active session count
- \`DELETE /auth/me/devices/{device_id}\` — revoke device + sessions
- \`PATCH /auth/me/devices/{device_id}\` — update display_name

## Out of scope

- RF-54: new-device push/email notifications
- Frontend UI
- Admin RBAC device management
- MFA

## Branch

\`feat/auth-device-session-management\`
" \
  --label "enhancement"
```

Note the issue number printed. Use it in the PR body.

- [ ] **Step 2: Create the runbook**

Create `docs/runbooks/task-device-session-management.md`:

```markdown
# Runbook: Device and Session Management

**RF traceability:**

| ID    | Requirement                                       | Implementation                                                                                                          |
| ----- | ------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------- |
| RF-51 | Sessions expire after inactivity                  | `idle_expires_at` / `absolute_expires_at` stored in `auth.user_sessions`, surfaced in `GET /auth/me/sessions` response  |
| RF-52 | Multi-device limit configurable by admin          | `auth_policy_settings.max_devices_per_user`; surfaced in `GET /auth/me/devices` response as `meta.max_devices_per_user` |
| RF-53 | Linked device list visible and manageable by user | All 6 endpoints in this runbook                                                                                         |
| RF-54 | New-device notifications                          | **Out of scope** — not implemented; field `user_devices.trusted_at` preserved for future use                            |

---

## Endpoints

All endpoints require `Authorization: Bearer <access_token>`.

### GET /auth/me/sessions

Returns the authenticated user's sessions, newest first. Active only by default.

**Query params:**

- `?include_revoked=true` — include revoked sessions
- `?limit=N` — number of sessions (default 50, max 100)

**Response fields:**

- `id` — session UUID
- `device_id` — linked device UUID or null
- `created_at`, `last_seen_at`, `idle_expires_at`, `absolute_expires_at` — RFC3339 timestamps
- `revoked_at` — null if active
- `ip_address` — masked (IPv4: `a.b.*.*`; IPv6: `prefix:*`)
- `user_agent` — truncated to 200 chars, non-printable stripped
- `current` — true if this is the session from the current access token

**Privacy:** Raw IP and user_agent are never returned. `refresh_token_hash`, `device_fingerprint_hash`, and `password_hash` are never returned.

---

### DELETE /auth/me/sessions/{session_id}

Revokes a single session. Also revokes active `refresh_token_history` for that session.

- `204` — revoked (or own already-revoked session, idempotent)
- `400` — malformed UUID
- `404` — unknown or cross-user session

---

### DELETE /auth/me/sessions

Revokes all sessions except the current one.

- `204` — always on success
- `409` — access token does not carry a `sid` claim (defense-in-depth guard)

**Note on stateless JWT:** Revocation takes effect immediately in the database. However, since `BearerAuth` is stateless (validates JWT signature and expiry only), a revoked session's **access token** remains valid until its `exp` claim expires (typically 15 minutes). After expiry or on the next `POST /auth/refresh`, the revocation fully takes effect.

---

### GET /auth/me/devices

Returns the authenticated user's linked devices, newest-last-seen first. Active only by default.

**Query params:**

- `?include_revoked=true` — include revoked devices
- `?limit=N` — default 50, max 100

**Response fields:**

- `id`, `display_name`, `platform`
- `last_ip` — masked
- `first_seen_at`, `last_seen_at`, `revoked_at`
- `session_count` — active sessions for this device
- `current` — true if the current access token's session belongs to this device
- **`meta.max_devices_per_user`** — from `auth_policy_settings`

---

### DELETE /auth/me/devices/{device_id}

Revokes the device, all its active sessions, and their active `refresh_token_history` rows in a single CTE transaction.

- `204` — revoked (or own already-revoked device, idempotent)
- `400` — malformed UUID
- `404` — unknown or cross-user device

---

### PATCH /auth/me/devices/{device_id}

Updates `display_name` of an **active** device.

**Request body:** `{"display_name": "My Laptop"}`

- `204` — updated
- `400` — malformed UUID or invalid display_name (empty, >80 chars, or control-char-only)
- `404` — unknown, revoked, or cross-user device

**Validation:** `display_name` is trimmed, control characters (NUL, CR, LF, etc.) stripped, 1–80 chars enforced.

---

## Security Notes

- All queries use `WHERE user_id = $authenticated_user_id` at SQL level — cross-user access is impossible.
- Parameterized SQL only — no string formatting in queries.
- IPs masked consistently with `/auth/me/login-attempts` endpoint.
- `device_fingerprint_hash`, `refresh_token_hash`, `password_hash`, raw tokens never returned or logged.

---

## Out of Scope

- Frontend UI
- Admin RBAC for device management (future)
- RF-54: push/email notification for new device (future)
- MFA
- Hard-delete / anonymization of session/device records
- Device fingerprint enforcement for sessions without fingerprint
```

- [ ] **Step 3: Update README auth section**

In `README.md`, find the auth endpoints table (search for `RouteAuthMeLoginAttempts` or `/auth/me/login-attempts`) and append the new endpoints. Add this block after the login-attempts row:

```markdown
| `GET /auth/me/sessions` | Bearer | List own sessions (active by default; `?include_revoked=true`) |
| `DELETE /auth/me/sessions/{session_id}` | Bearer | Revoke one session (204; 404 cross-user) |
| `DELETE /auth/me/sessions` | Bearer | Revoke all sessions except current (409 if no sid in token) |
| `GET /auth/me/devices` | Bearer | List own devices with session count and `max_devices_per_user` |
| `DELETE /auth/me/devices/{device_id}` | Bearer | Revoke device and all its sessions |
| `PATCH /auth/me/devices/{device_id}` | Bearer | Update device `display_name` (1–80 chars) |
```

Also add an RF traceability subsection:

```markdown
### RF Traceability

| RF    | Requirement                              | Endpoint                                                                   |
| ----- | ---------------------------------------- | -------------------------------------------------------------------------- |
| RF-51 | Session expires after inactivity         | `GET /auth/me/sessions` surfaces `idle_expires_at` / `absolute_expires_at` |
| RF-52 | Multi-device limit configurable by admin | `GET /auth/me/devices` returns `meta.max_devices_per_user`                 |
| RF-53 | Linked device list visible/manageable    | All `/auth/me/sessions` and `/auth/me/devices` endpoints                   |
| RF-54 | New-device notification                  | **Out of scope** in this PR                                                |
```

- [ ] **Step 4: Run format check**

```bash
pnpm format:check:docs
```

Expected: passes (or fix any trailing whitespace/newline issues flagged).

- [ ] **Step 5: Commit**

```bash
git add docs/runbooks/task-device-session-management.md README.md
git commit -m "docs(auth): add device/session management runbook and README update

RF traceability: RF-51, RF-52, RF-53. RF-54 documented as out of scope.
Runbook covers all 6 endpoints, privacy masking, idempotency, and JWT limitation.

Co-authored-by: Copilot <223556219+Copilot@users.noreply.github.com>"
```

---

## Task 10: CI Validation and PR

**Files:** none new

- [ ] **Step 1: Run full test suite**

```bash
cd services/auth-service && go test -count=1 ./...
```

Expected: all tests PASS with no failures.

- [ ] **Step 2: Run formatting and linting**

```bash
pnpm fmt:go
pnpm lint:go
pnpm vet:go
```

Expected: no errors, no diffs.

- [ ] **Step 3: Run coverage check**

```bash
pnpm test:coverage:go:check
```

Expected: passes coverage threshold.

- [ ] **Step 4: Run migration check**

```bash
pnpm migrations:check
bash -n scripts/db/migrate.sh scripts/ci/migrations-check.sh 2>/dev/null || true
```

Expected: no syntax errors.

- [ ] **Step 5: Run CI**

```bash
pnpm run ci
```

Expected: PASS.

- [ ] **Step 6: Security scan**

```bash
semgrep scan \
  --config p/owasp-top-ten \
  --config p/secrets \
  services/auth-service \
  migrations/auth \
  docs/runbooks/task-device-session-management.md
```

Expected: no findings in new code. If findings appear, address them before opening PR.

- [ ] **Step 7: Push branch**

```bash
git push -u origin feat/auth-device-session-management
```

- [ ] **Step 8: Open PR**

```bash
gh pr create \
  --repo nicrepository/nchat \
  --base develop \
  --title "feat(auth): add device and session management endpoints" \
  --body "## Summary

Implements authenticated REST endpoints for session and device management.

Closes #<ISSUE_NUMBER>

## RF Traceability

| RF | Requirement | Implementation |
|----|-------------|----------------|
| RF-51 | Sessions expire after inactivity | \`idle_expires_at\` / \`absolute_expires_at\` surfaced in session list |
| RF-52 | Multi-device limit configurable | \`meta.max_devices_per_user\` in device list |
| RF-53 | Device list visible/manageable | All 6 endpoints |
| RF-54 | New-device notification | **Out of scope** |

## Endpoints

| Method | Path | Description |
|--------|------|-------------|
| GET | \`/auth/me/sessions\` | List own sessions; active by default |
| DELETE | \`/auth/me/sessions/{session_id}\` | Revoke one session |
| DELETE | \`/auth/me/sessions\` | Revoke all except current |
| GET | \`/auth/me/devices\` | List own devices with session count |
| DELETE | \`/auth/me/devices/{device_id}\` | Revoke device + sessions |
| PATCH | \`/auth/me/devices/{device_id}\` | Update display_name |

## Security

- Cross-user access impossible at SQL level (\`WHERE user_id = \$authenticated_user_id\`)
- IPs masked consistently with existing login-attempts endpoint
- User-agent sanitized (truncated to 200 chars, control chars stripped)
- \`device_fingerprint_hash\`, \`refresh_token_hash\`, \`password_hash\` never returned
- Parameterized SQL throughout
- RevokeDevice uses CTE — cascades to sessions and refresh_token_history atomically
- DELETE /auth/me/sessions returns 409 when access token has no sid claim

## JWT Limitation

BearerAuth is stateless. A revoked session's access token remains valid until \`exp\`. This is documented in the runbook and intentionally not changed in this PR to avoid scope creep.

## Idempotency

| Operation | Own already-revoked | Cross-user |
|-----------|---------------------|------------|
| DELETE session/{id} | 204 | 404 |
| DELETE sessions (bulk) | 204 | n/a |
| DELETE device/{id} | 204 | 404 |
| PATCH device/{id} | 404 (revoked) | 404 |

## Out of Scope

- Frontend UI
- Admin RBAC for devices
- RF-54: new-device notifications
- MFA
- DB-backed BearerAuth session validation

## Test Plan

- GET /auth/me/sessions requires Bearer → 401 without
- GET /auth/me/sessions returns only own sessions (userID SQL guard)
- Sessions response hides refresh_token_hash, device_fingerprint_hash
- DELETE own session → 204, revokes session + refresh_token_history
- DELETE cross-user session → 404
- GET /auth/me/devices requires Bearer → 401 without
- GET /auth/me/devices returns only own devices; includes session_count, max_devices_per_user
- DELETE own device → 204, revokes device + sessions + refresh history (CTE verified)
- DELETE cross-user device → 404
- Device with no active sessions: RevokeDevice still returns 204
- IP masking: 1.2.3.4 → 1.2.*.* | ::1 → ::*
- User-agent truncation verified
- Invalid UUID → 400 before SQL
- PATCH revoked device → 404
- PATCH display_name validation: empty → 400, >80 chars → 400, control chars stripped
" \
  --draft
```

Expected: PR created in draft mode. Review before marking ready.
