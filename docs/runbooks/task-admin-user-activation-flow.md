# Admin User Status Foundation — Runbook

**Branch:** feat/admin-user-activation-flow
**Implements:** RF-75 foundation (partial)
**Prerequisite for end-to-end:** RF-74 (admin JWT/RBAC guard)

---

## Summary

This PR adds the backend infrastructure for admin-controlled user account suspension
and reactivation. The end-to-end browser flow is **not yet enabled** — it depends on
a JWT-based admin authorization guard (RF-74) that does not yet exist.

---

## What is implemented

### Backend

- `PATCH /admin/users/{id}/status` — accepts `{ "status": "active" | "suspended" }`.
- Permitted transitions: `active → suspended`, `suspended → active` only.
- Guarded by `AdminBootstrapGuard` (`X-NChat-Admin-Token` header). **Not browser-callable.**
- On suspension: all active sessions and refresh tokens for the target user are revoked
  (`revoked_reason = 'admin_suspension'`).
- On activation: no sessions are restored. The user must log in again.
- Returns `userResponse` shape (same as `POST /admin/users`).

### Frontend

- `updateUserStatus(id, status)` typed function added to `adminUsersApi.ts`.
  Prepared for use when a browser-callable admin JWT/RBAC endpoint is available.
  **Not called from the UI in this PR.**
- Action buttons ("Suspender" / "Ativar") are rendered in the users table but are
  permanently disabled with `title="Requer permissão de administrador"`.

---

## Session revocation behavior

When an admin suspends a user:
1. `UPDATE auth.user_sessions SET revoked_at = now(), revoked_reason = 'admin_suspension'`
   for all non-revoked sessions of that user.
2. `UPDATE auth.refresh_token_history SET status = 'revoked'` for all active refresh
   token history rows in those sessions.

Even without explicit revocation, suspended users are blocked by:
- Login: `login_store.go` rejects `status != 'active'`.
- Refresh: `session_store.go` JOIN requires `u.status = 'active'`.
- Session validation: `device_session_store.go` `ValidateActiveSession` requires `u.status = 'active'`.

On activation, no sessions are restored. The user must authenticate again.

---

## Authorization limitations

The current `PATCH /admin/users/{id}/status` endpoint is guarded by `AdminBootstrapGuard`,
a static shared token. Limitations:

| Limitation | Mitigation |
|-----------|------------|
| Token is not browser-safe | Endpoint is only for server-to-server / tooling use |
| No user identity in request | callerID passed as `""` — self-deactivation not enforced |
| No RBAC role check | Any holder of ADMIN_BOOTSTRAP_TOKEN can call this |

**Self-deactivation prevention** requires the requesting admin's user ID from a verified JWT.
This will be enforced when RF-74 provides a `BearerAuth` + admin role check, at which point
`callerID` in `UserService.UpdateUserStatus` will be populated from the JWT `sub` claim.

---

## Enabling the browser flow (future work — RF-74)

To enable the frontend action buttons:

1. Implement an admin JWT guard: `BearerAuth` + admin role claim or `is_admin` flag.
2. Add a new route (or replace the bootstrap route) that uses this JWT guard.
3. Remove the `disabled` / `aria-disabled` attributes from `StatusActionButton`.
4. Wire click handler → `updateUserStatus()` → confirmation dialog → mutation state.
5. Add self-deactivation test: `callerID` from JWT `sub` == `targetID` → 403.

---

## Out of scope

- End-to-end browser mutation (requires RF-74)
- Self-deactivation prevention (requires callerID from JWT)
- Hard delete / soft delete / LGPD erasure
- `locked`, `invited`, `deleted` status handling via this flow
- Role assignment / RBAC matrix
- User creation / edit / invite / password reset from admin panel
- Audit log export / bulk operations
