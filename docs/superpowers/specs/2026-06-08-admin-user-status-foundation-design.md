# Design: Admin User Status Foundation

**Date:** 2026-06-08
**Branch:** feat/admin-user-activation-flow
**Target PR:** develop
**Related requirements:** RF-75 (foundation), RF-73/RF-74 (prerequisite)

---

## 1. Scope and framing

This document describes the **foundation** for admin-controlled user status changes (suspension and reactivation). It is **not** a complete end-to-end activation/deactivation flow. The end-to-end flow requires an admin JWT/RBAC guard (RF-74) that does not yet exist.

### What this PR delivers

| Component                                                           | Status                                     |
| ------------------------------------------------------------------- | ------------------------------------------ |
| Domain: status transition validation (`active ↔ suspended`)         | ✅ Implemented                             |
| Storage: `GetUserByID`, `UpdateUserStatus`, `RevokeAllUserSessions` | ✅ Implemented                             |
| Service: `UpdateUserStatus` with session revocation on suspension   | ✅ Implemented                             |
| HTTP: `PATCH /admin/users/{id}/status` behind `AdminBootstrapGuard` | ✅ Implemented (not browser-callable)      |
| Frontend: `updateUserStatus()` typed contract in `adminUsersApi.ts` | ✅ Prepared                                |
| Frontend: action buttons rendered but **disabled**                  | ✅ Disabled with dependency label          |
| End-to-end mutation via browser                                     | ❌ Blocked — requires admin JWT/RBAC guard |
| Self-deactivation prevention (reliable)                             | ❌ Requires callerID from JWT (RBAC)       |
| RF-74 RBAC / role assignment                                        | ❌ Out of scope                            |
| Hard delete / LGPD erasure                                          | ❌ Out of scope                            |

---

## 2. Authorization decision

### Current state

The only admin guard in the codebase is `AdminBootstrapGuard`, which validates a static `X-NChat-Admin-Token` header. This token:

- Is a server-side secret
- Cannot and must not be exposed to the browser
- Does not carry user identity (no callerID)

The frontend uses `authenticatedFetch`, which injects `Authorization: Bearer <JWT>`. There is no admin role/claim in the JWT, no `is_admin` flag in the DB, and no RBAC layer.

### Decision: Option B (fail-closed foundation)

The `PATCH /admin/users/{id}/status` endpoint is guarded by `AdminBootstrapGuard`. It is **not browser-callable** in this PR. The frontend displays action buttons as disabled until a JWT-based admin guard exists.

This is consistent with the existing pattern: `POST /admin/users` and `POST /admin/invites` follow the same guard.

### Self-deactivation

Self-deactivation prevention requires a reliable callerID from an authenticated JWT. Since `AdminBootstrapGuard` provides no user identity, self-deactivation is **not enforced in this PR**. The `UpdateUserStatus` service method accepts a `callerID string` parameter that is left empty (`""`) by the handler in this PR. When callerID is non-empty and matches the target, the service returns `ErrForbidden`. This contract is ready for the RBAC-enabled handler.

---

## 3. Status model

### Permitted values (existing DB constraint)

```sql
CHECK (status IN ('active', 'invited', 'suspended', 'locked', 'deleted'))
```

### Permitted transitions in this flow

| From        | To          | Allowed                            |
| ----------- | ----------- | ---------------------------------- |
| `active`    | `suspended` | ✅                                 |
| `suspended` | `active`    | ✅                                 |
| Any         | Any other   | ❌ `ErrStatusTransitionNotAllowed` |

`locked` remains the brute-force lockout status and is not touched by this flow. `deleted`, `invited` are out of scope.

### No migration required

The `status` column and its constraint already exist in `migrations/auth/000001_auth_identity_schema.up.sql`.

---

## 4. Backend design

### 4.1 Domain layer (`services/auth-service/internal/domain/`)

**`errors.go`** — new sentinel errors:

```go
var ErrStatusTransitionNotAllowed = errors.New("status transition not allowed")
var ErrForbidden                  = errors.New("forbidden")
```

**`user.go`** — new transition validator:

```go
// ValidateStatusTransition returns ErrStatusTransitionNotAllowed for any
// transition not in the permitted set. Only active↔suspended is allowed.
func ValidateStatusTransition(from, to string) error
```

### 4.2 Storage layer (`services/auth-service/internal/storage/`)

**`user_store.go`** — extend `UserStore` interface:

```go
type UserStore interface {
    CreateUser(...)          // existing
    GetPolicySettings(...)   // existing
    GetUserByID(ctx context.Context, id string) (domain.User, error)
    UpdateUserStatus(ctx context.Context, id, status string) (domain.User, error)
}
```

`PGXUserStore` implements both:

- `GetUserByID`: `SELECT ... FROM auth.users WHERE id = $1 AND deleted_at IS NULL`
- `UpdateUserStatus`: `UPDATE auth.users SET status = $2, updated_at = now() WHERE id = $1 AND deleted_at IS NULL RETURNING ...`; returns `domain.ErrNotFound` if no row matched.

**`device_session_store.go`** — extend interface and add method:

```go
// In DeviceManager interface (or a new SessionRevoker interface):
RevokeAllUserSessions(ctx context.Context, userID string) error
```

`PGXDeviceSessionStore.RevokeAllUserSessions`: CTE-based revocation of all active sessions for `userID` (no exception), setting `revoked_at = now()`, `revoked_reason = 'admin_suspension'`, and marking active refresh token history as `'revoked'`.

### 4.3 Service layer (`services/auth-service/internal/service/`)

**`user_service.go`** — new interface and method:

```go
// UserStatusManager is the HTTP-facing interface for status change operations.
type UserStatusManager interface {
    UpdateUserStatus(ctx context.Context, callerID, targetID, newStatus string) (domain.User, error)
}
```

`UserService.UpdateUserStatus` logic:

1. `GetUserByID(ctx, targetID)` → wrap as `ErrNotFound` if absent
2. `ValidateStatusTransition(user.Status, newStatus)` → propagate `ErrStatusTransitionNotAllowed`
3. If `callerID != "" && callerID == targetID` → return `ErrForbidden`
4. `store.UpdateUserStatus(ctx, targetID, newStatus)`
5. If `newStatus == "suspended"` → `sessionRevoker.RevokeAllUserSessions(ctx, targetID)`
6. Return updated user

`UserService` requires a `SessionRevoker` dependency (injected via constructor). This is a new field.

### 4.4 HTTP layer (`services/auth-service/internal/http/`)

**`routes.go`**:

```go
RouteAdminUserStatus = "/admin/users/{id}/status"
```

**`admin_handler.go`** — `AdminUpdateUserStatus(users service.UserStatusManager) http.Handler`:

- `nil` service → 503
- Extract `{id}` from path; validate non-empty
- Decode `{"status": "active"|"suspended"}` — unknown values → 422 with `"invalid_status"`
- Call `users.UpdateUserStatus(ctx, "", id, status)` (callerID is empty — no RBAC)
- Error mapping:
  - `ErrNotFound` → 404
  - `ErrStatusTransitionNotAllowed` → 422
  - `ErrForbidden` → 403 (future use)
  - other → 500
- Success → 200 + `userResponse` (same shape as `AdminCreateUser`)

**`router.go`**:

```go
mux.Handle(RouteAdminUserStatus, httputil.MethodNotAllowed(http.MethodPatch,
    AdminBootstrapGuard(cfg.AdminBootstrapToken)(AdminUpdateUserStatus(users)),
))
```

`NewRouter` receives the `users` dependency; it already exists as `service.UserCreator`. The type will be widened to accept `UserStatusManager` (or `UserService` which implements both) — the cleanest approach is to pass `*UserService` directly or define a combined interface.

---

## 5. Frontend design

### 5.1 `apps/web/src/admin/adminUsersApi.ts`

Add a typed `updateUserStatus` function:

```ts
/**
 * Updates the status of an admin user.
 *
 * NOTE: This function is prepared for when a browser-callable admin JWT/RBAC
 * guard exists. The corresponding backend endpoint is currently only accessible
 * via AdminBootstrapGuard (X-NChat-Admin-Token), which is not browser-safe.
 * Do not call this function from the UI until the guard is in place.
 */
export async function updateUserStatus(
  id: string,
  status: "active" | "suspended",
): Promise<AdminUser>;
```

Uses `authenticatedFetch`. Returns `AdminUser` on success; propagates `ApiRequestError` on failure. Does **not** swallow errors as empty results.

### 5.2 `apps/web/src/admin/AdminUsersPage.tsx`

Add an "Ações" column to the users table. Action buttons are rendered but always disabled:

| User status | Button label | State                                                                           |
| ----------- | ------------ | ------------------------------------------------------------------------------- |
| `active`    | "Suspender"  | `disabled`, `aria-disabled="true"`, `title="Requer permissão de administrador"` |
| `suspended` | "Ativar"     | `disabled`, `aria-disabled="true"`, `title="Requer permissão de administrador"` |
| other       | (none)       | —                                                                               |

No confirmation dialog, no mutation state, no loading/error handling in this PR — the buttons cannot be activated.

The existing invite button pattern (`disabled`, `aria-disabled="true"`, `title="Funcionalidade não disponível nesta versão"`) is the model to follow.

---

## 6. Effect on login / session / refresh

The following behaviors already hold because of existing DB checks; no changes required:

| Operation               | Suspended user outcome                                |
| ----------------------- | ----------------------------------------------------- |
| `POST /auth/login`      | ❌ `status != 'active'` check in `login_store.go:79`  |
| `POST /auth/refresh`    | ❌ `u.status = 'active'` JOIN in `session_store.go`   |
| `ValidateActiveSession` | ❌ `u.status = 'active'` in `device_session_store.go` |

Additionally, on suspension this PR revokes all existing sessions/refresh tokens via `RevokeAllUserSessions`, making the revocation explicit and audit-friendly.

On activation, no sessions are restored. The user must log in again.

---

## 7. Tests

### Backend (no real DB — fakes/stubs only)

**Service tests** (`service/user_service_test.go`):

- active → suspended succeeds
- suspended → active succeeds
- active → active rejected (`ErrStatusTransitionNotAllowed`)
- suspended → suspended rejected
- unknown user returns `ErrNotFound`
- suspension calls `RevokeAllUserSessions`
- activation does NOT call `RevokeAllUserSessions`
- callerID == targetID returns `ErrForbidden`

**Handler tests** (`http/admin_handler_test.go`):

- 200 + correct shape on success
- 503 when service is nil
- 404 on `ErrNotFound`
- 422 on `ErrStatusTransitionNotAllowed`
- 422 on unknown status value in body
- 400 on invalid JSON
- 500 on unexpected error
- 401 when `X-NChat-Admin-Token` is absent or wrong (via `AdminBootstrapGuard` — existing middleware tests cover this; add integration test for the wired route)

**Domain tests** (`domain/validation_test.go` or new file):

- `ValidateStatusTransition("active", "suspended")` → nil
- `ValidateStatusTransition("suspended", "active")` → nil
- `ValidateStatusTransition("active", "active")` → `ErrStatusTransitionNotAllowed`
- `ValidateStatusTransition("locked", "active")` → `ErrStatusTransitionNotAllowed`
- etc.

### Frontend

**`adminUsersApi.test.ts`**:

- `updateUserStatus` calls `PATCH /api/admin/users/{id}/status` with correct body
- Maps snake_case response to `AdminUser`
- Propagates non-2xx errors (does not swallow as empty)

**`AdminUsersPage.test.tsx`** (additions to existing tests):

- Active user row has a "Suspender" button that is `disabled`
- Suspended user row has an "Ativar" button that is `disabled`
- Buttons have `title` containing "Requer permissão de administrador"
- Row with status other than active/suspended has no action button
- No `X-NChat-Admin-Token` appears anywhere in the source

---

## 8. Docs

**`docs/runbooks/task-admin-user-activation-flow.md`** documents:

- RF-75 partial foundation: backend logic complete, end-to-end pending RBAC
- `PATCH /admin/users/{id}/status` behind `AdminBootstrapGuard` (not browser-callable)
- Frontend displays disabled actions until admin JWT/RBAC guard (RF-74)
- Session revocation on suspension behavior
- Activation does not restore sessions
- Self-deactivation prevention requires future RBAC callerID
- Hard delete / LGPD / role assignment out of scope

---

## 9. Out of scope

- End-to-end browser mutation (requires RF-74 admin JWT/RBAC)
- Self-deactivation prevention (requires reliable callerID from JWT)
- Hard delete / soft delete / LGPD erasure
- `locked`, `invited`, `deleted` status handling in this flow
- Role assignment / RBAC matrix
- User creation / edit / invite / password reset from admin
- Audit log export
- Bulk operations
