# Admin User Status Foundation — Runbook

**Branch:** feat/admin-user-activation-flow
**Implements:** RF-75 foundation (partial)
**Prerequisite for end-to-end browser flow:** RF-74 (admin JWT/RBAC guard)

---

## Summary

This PR implements the **backend status transition and session revocation foundation** for admin-controlled user suspension and reactivation (RF-75, partial). The end-to-end browser flow is **not enabled in this PR** — it depends on a JWT-based admin authorization guard (RF-74) that does not yet exist.

The frontend displays disabled action buttons as a UI contract placeholder only. The mutation endpoint is not browser-callable.

---

## What is implemented

### Backend status transition + session revocation

- `PATCH /admin/users/{id}/status` — accepts `{ "status": "active" | "suspended" }`.
- Permitted transitions: `active → suspended`, `suspended → active` only.
- Guarded by `AdminBootstrapGuard` (`X-NChat-Admin-Token` header). **Not browser-callable. Not final RF-74/RBAC.**
- Status update and session revocation execute in a **single atomic DB transaction**:
  - On `suspended`: user status updated + all active sessions revoked + refresh token history marked revoked (`admin_suspension`).
  - On `active`: user status updated only. No sessions restored. User must log in again.
- Returns `userResponse` shape (same as `POST /admin/users`).
- OIDC exchange codes minted before suspension cannot produce tokens for suspended users (user status is revalidated at exchange consumption time).

### Frontend contract (disabled)

- `updateUserStatus(id, status)` typed function added to `adminUsersApi.ts`.
  Prepared for when a browser-callable admin JWT/RBAC endpoint exists.
  **Not triggered from the UI in this PR.**
- Action buttons ("Suspender" / "Ativar") are rendered but permanently disabled
  with `title="Requer permissão de administrador"`.
- `X-NChat-Admin-Token` is **not sent by runtime frontend code**.
  It may appear in tests or comments as a boundary assertion (e.g., asserting it is absent), which is expected.

---

## Session and token revocation behavior

RF-75 partial foundation (status transition + revocation atomics). End-to-end browser admin flow depends on future RF-74/RBAC admin guard.

Status change, session revocation, and OIDC exchange invalidation happen **atomically in a single DB transaction**:

1. User row locked (`SELECT ... FOR UPDATE`).
2. Transition validated under the lock (`active → suspended` or `suspended → active`).
3. User status updated.
4. If `suspended`:
   - `UPDATE auth.user_sessions SET revoked_at = now(), revoked_reason = 'admin_suspension'` for all non-revoked sessions + cascade to `auth.refresh_token_history`.
   - `UPDATE auth.oidc_exchange_codes SET used_at = now() WHERE used_at IS NULL AND expires_at > now() AND user_json->>'id' = $1` — invalidates pending OIDC exchange codes so a pre-suspension code cannot be consumed after reactivation.
5. Commit. If any step fails, the entire transaction rolls back — no partial state.

**Activation** updates status only. No sessions, refresh tokens, or OIDC exchange codes are restored. The user must log in again.

### Password login / suspension race

Password login re-validates user status immediately after password verification but **before inserting any session artifact**, using `SELECT ... FOR UPDATE` on the user row. This serializes with the suspension transaction's own row lock:

- If suspension commits first: login revalidation sees `status != 'active'`, returns generic `ErrInvalidCredentials`, no session created.
- If login proceeds first: login holds the row lock through session insert; suspension then waits, locks the row, and revokes the newly-created session in its own transaction.

### Other guards for suspended users

- Refresh: `session_store.go` JOIN requires `u.status = 'active'`.
- Session validation: `device_session_store.go` requires `u.status = 'active'`.
- OIDC exchange consumption: `ConsumeExchange` joins `auth.users` and rejects inactive users.

---

## Authorization limitations

The current `PATCH /admin/users/{id}/status` endpoint is guarded by `AdminBootstrapGuard` (static shared `X-NChat-Admin-Token`). This is a **bootstrap-only** guard, not the final RF-74/RBAC implementation.

| Limitation                  | Status                                                     |
| --------------------------- | ---------------------------------------------------------- |
| Token is not browser-safe   | Endpoint is only for server-to-server / tooling use        |
| No user identity in request | `callerID` passed as `""` — self-deactivation not enforced |
| No RBAC role check          | Any holder of `ADMIN_BOOTSTRAP_TOKEN` can call this        |

**Self-deactivation prevention** requires the requesting admin's user ID from a verified JWT. This will be enforced when RF-74 provides a `BearerAuth` + admin role check, at which point `callerID` in `UserService.UpdateUserStatus` will be populated from the JWT `sub` claim.

---

## Enabling the browser flow (future work — RF-74)

1. Implement an admin JWT guard: `BearerAuth` + admin role claim or `is_admin` flag.
2. Add or replace the route with the JWT guard.
3. Remove `disabled` / `aria-disabled` attributes from `StatusActionButton` in `AdminUsersPage.tsx`.
4. Wire click → `updateUserStatus()` → confirmation dialog → mutation state.
5. Populate `callerID` from JWT `sub` claim in the handler to enforce self-deactivation.

---

## Validation commands

```bash
cd services/auth-service && go test -count=1 ./...
pnpm fmt:go && pnpm lint:go && pnpm vet:go
pnpm test:coverage:go:check
pnpm format:check:web && pnpm lint:web && pnpm typecheck:web
pnpm test:web && pnpm test:coverage:web
pnpm format:check:docs
pnpm migrations:check
git diff --check origin/develop...HEAD
semgrep scan --config p/secrets --config p/owasp-top-ten --config p/golang services/auth-service apps/web docs README.md
```

---

## Out of scope

- End-to-end browser mutation (requires RF-74)
- Self-deactivation prevention end-to-end (requires callerID from JWT)
- Hard delete / soft delete / anonymization / LGPD erasure
- `locked`, `invited`, `deleted` status handling via this flow
- Role assignment / RBAC matrix / provider management
- User creation / edit / invite / password reset from admin panel
- Audit log export / bulk activation-deactivation
