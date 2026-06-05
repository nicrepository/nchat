# Admin Users Basic Screen — Design Spec

**Date:** 2026-06-05  
**Status:** Approved  
**Branch:** feat/web-admin-users-basic  
**PR target:** develop

---

## Problem

Administrators need a read-only overview of all users registered in the workspace. Currently there is no admin UI in the web frontend.

## Scope

**In scope:**

- `/admin/users` route protected by `RequireAuth`
- Page title, user table/list with name, email, status badge, role/permission, created at
- Loading state (skeleton rows)
- Empty state
- Generic error state
- Frontend API client boundary (`adminUsersApi.ts`)

**Out of scope:**

- Backend `GET /admin/users` endpoint (no endpoint exists yet)
- CRUD, invite, role assignment, password reset
- RBAC implementation
- Audit log, pagination contract, admin layout overhaul

## Architecture

```
apps/web/src/
  admin/
    adminUsersApi.ts       ← typed client; uses authenticatedFetch
    AdminUsersPage.tsx     ← page component (table + states)
    AdminUsersPage.css     ← styles using design tokens
    AdminUsersPage.test.tsx
  App.tsx                  ← adds /admin/users route under RequireAuth
docs/runbooks/
  task-frontend-admin-users-basic.md
```

## API Client Contract

`listAdminUsers()` calls `GET /api/admin/users` via `authenticatedFetch`.

- 200 OK → returns `AdminUser[]`
- 404 Not Found → returns `[]` (endpoint not yet deployed; shows empty state, not error)
- Other errors → throws; page shows generic error state

Since the backend does not yet expose `GET /admin/users`, the page renders empty state by default. No demo data, no hardcoded real users.

## Design

Follows NIC Chat design tokens (`tokens.css`):

- Primary: `#0e7490` (teal)
- Surfaces, text, borders as defined in tokens
- Table on ≥640 px, stacked cards on mobile
- Avatar: initials derived from `displayName` (first two uppercase letters of first two words)
- Status badge: `active` → success, `suspended` → danger, default → neutral
- Loading: skeleton shimmer rows
- Empty: centered icon + message
- Error: centered warning icon + message

## Security Notes

- `/admin/users` frontend route is a UX guard only — **not an authorization boundary**
- Backend authorization is mandatory and must be enforced server-side
- No tokens in localStorage, no tokens in URLs
- `authenticatedFetch` handles Bearer token injection and refresh
