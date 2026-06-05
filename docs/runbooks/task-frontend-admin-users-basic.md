# Admin Users Basic Screen — Runbook

**Feature:** Basic read-only admin users list in the web frontend |
**Branch:** `feat/web-admin-users-basic` |
**Status:** In progress

---

## Overview

This runbook documents the implementation of the basic admin users screen at
`/admin/users` in the `apps/web` frontend.

The screen displays a read-only table of workspace users. It is the first step
toward a full admin UI and intentionally limits scope to **viewing** only.

---

## Prototype Reference

The visual design follows:

- `prototype/claude-design-v1/nic-chat/admin-usuarios.html`
- `prototype/claude-design-v1/nic-chat/admin.html`
- `prototype/claude-design-v1/nic-chat/tokens.css`

The page uses the NIC Chat design tokens (`apps/web/src/tokens.css`) — teal
primary brand (`#0e7490`), not a generic purple SaaS palette.

---

## Authorization Boundary Warning

> **The frontend route is a UX guard only — it is NOT an authorization boundary.**

`RequireAuth` ensures the user has a valid session before the route renders.
It does **not** enforce admin permissions. Real authorization must be enforced
by the backend on every API call.

This is by design: the frontend cannot be trusted to enforce access control.
Backend services must validate that the caller has admin privileges on every
request to `/admin/*` endpoints.

---

## Backend Endpoint Status

The backend **does not yet expose** `GET /admin/users`. The current
`/admin/users` route only handles `POST` (user creation, bootstrap-guarded).

The frontend client (`adminUsersApi.ts`) handles this gracefully:

- **200 OK** → renders user rows
- **404 Not Found** → returns empty array → renders empty state (not error)
- **Other errors** → renders generic error state

No demo data or hardcoded user records are included.

---

## UI Alignment Follow-Up

A follow-up change (`fix/web-admin-users-match-prototype`) aligns the `/admin/users`
screen with the Claude Design prototype (`prototype/claude-design-v1/nic-chat/`).

### What changed

| File                                         | Change                                              |
| -------------------------------------------- | --------------------------------------------------- |
| `apps/web/src/admin/AdminShell.tsx`          | New: dark sidebar + admin top tabnav shell          |
| `apps/web/src/admin/AdminShell.css`          | New: shell styles (scoped prototype purple tokens)  |
| `apps/web/src/admin/AdminUsersPage.tsx`      | Wrapped in AdminShell; filter chips; search; button |
| `apps/web/src/admin/AdminUsersPage.css`      | Updated styles (page head, chips, toolbar, icons)   |
| `apps/web/src/admin/AdminUsersPage.test.tsx` | Added shell, nav, invite, filter, and search tests  |

### What the alignment covers

- Dark left sidebar with NIC Chat / Workspace NIC-Labs branding
- Admin top navigation: Visão geral, **Usuários** (active), Canais, Auditoria
- Page head with title, subtitle, and disabled "Convidar usuário" button
- Search input and filter chips (Todos, Ativos, Suspensos, Admins, Convites pendentes)
  - Client-side filtering for Todos / Ativos / Suspensos
  - Admins and Convites pendentes show empty state (no role/invite data in API)
- Empty and error states render inside the admin shell layout
- Emoji icons replaced with inline SVG
- Prototype purple (`#6D28D9`) scoped to `.admin-app` wrapper

### Authorization boundary note (unchanged)

This change is **frontend-only**. No RBAC, no backend admin support, no role inference
from `authSource`. The `RequireAuth` session guard and the read-only `adminUsersApi`
behavior are preserved. Real admin authorization must be enforced by the backend.

---

## Files Changed

| File                                                            | Change                                         |
| --------------------------------------------------------------- | ---------------------------------------------- |
| `apps/web/src/App.tsx`                                          | Added `/admin/users` route under `RequireAuth` |
| `apps/web/src/admin/adminUsersApi.ts`                           | Typed API client boundary                      |
| `apps/web/src/admin/AdminUsersPage.tsx`                         | Page component (table, loading, empty, error)  |
| `apps/web/src/admin/AdminUsersPage.css`                         | Page styles using design tokens                |
| `apps/web/src/admin/AdminUsersPage.test.tsx`                    | Vitest unit tests                              |
| `docs/runbooks/task-frontend-admin-users-basic.md`              | This runbook                                   |
| `docs/superpowers/specs/2026-06-05-admin-users-basic-design.md` | Design spec                                    |

---

## Out of Scope

The following are explicitly out of scope for this PR:

- Backend `GET /admin/users` endpoint
- Backend authorization changes
- RBAC implementation
- User creation, edit, delete, invite flow
- Role assignment, password reset from admin
- Audit log
- Server-side pagination
- Admin layout overhaul
- Provider management
- Mobile app

---

## Running Locally

```bash
cd apps/web
pnpm dev
# navigate to http://localhost:5173/admin/users
```

The page renders empty state by default (no backend endpoint).

---

## Validation Commands

```bash
# From repo root:
pnpm format:check:web
pnpm lint:web
pnpm typecheck:web
pnpm test:web
pnpm test:coverage:web
pnpm format:check:docs
git diff --check origin/develop...HEAD
```

### Playwright (E2E)

If Chromium cannot be installed locally (e.g., restricted Ubuntu environment),
Playwright E2E tests should run in CI. Document the local skip reason:

```bash
# If Chromium install fails:
# pnpm exec playwright install chromium --with-deps
# → Run E2E in CI instead (GitHub Actions workflow)
```

---

## Security Checklist

- [ ] No tokens in localStorage
- [ ] No tokens in URL parameters
- [ ] No hardcoded secrets or high-entropy fixtures
- [ ] No admin actions without backend support
- [ ] `authenticatedFetch` used for all protected API calls
- [ ] Backend authorization enforced server-side (out of scope for this PR)
