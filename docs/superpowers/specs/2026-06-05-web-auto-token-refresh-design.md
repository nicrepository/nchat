# Design: Web Automatic Token Refresh

**Date:** 2026-06-05
**Branch:** `feat/web-auto-token-refresh`
**Status:** Approved

## Context

The web frontend already stores `access_token` (`nchat_at`) and `refresh_token` (`nchat_rt`) in
`sessionStorage`. `RequireAuth` uses the refresh token to recover a session on mount (tab reload).
There is no mechanism to transparently refresh an expired access token mid-session while the user
is actively making API calls.

## Problem

When an authenticated API call receives a `401 Unauthorized` response because the access token has
expired, the user is currently left with a broken experience. The app neither retries the request
nor redirects cleanly — it just propagates the error to the caller.

## Chosen approach: `authenticatedFetch` in `lib/authClient.ts`

Add a new thin wrapper, `authenticatedFetch`, in `apps/web/src/lib/authClient.ts`. It wraps the
existing pure `apiFetch` utility with three responsibilities:

1. Inject `Authorization: Bearer <access_token>` header for every call.
2. Intercept `ApiRequestError(status=401)` on non-auth endpoints and attempt a single refresh.
3. On refresh success: store new tokens, retry the original request exactly once.
   On refresh failure: clear tokens, re-throw.

No changes to `api.ts`, `authSession.ts`, or `authApi.ts`. The existing auth functions
(`login`, `refresh`, `logout`, etc.) continue calling `apiFetch` directly and are never wrapped
by `authenticatedFetch`.

## Architecture

```
                 ┌────────────────────────────────────────┐
                 │  apps/web/src/lib/authClient.ts (NEW)  │
                 │                                        │
                 │  authenticatedFetch<T>(url, init)      │
                 │  ┌─────────────────────────────────┐   │
                 │  │ 1. inject Authorization header  │   │
                 │  │ 2. call apiFetch                │   │
                 │  │ 3. on 401 (non-auth):           │   │
                 │  │    - share inflightRefresh       │   │
                 │  │    - on success: setTokens,     │   │
                 │  │      retry once via apiFetch    │   │
                 │  │    - on failure: clearTokens,   │   │
                 │  │      rethrow                    │   │
                 │  └─────────────────────────────────┘   │
                 └────────┬───────────────┬───────────────┘
                          │               │
               ┌──────────▼──┐   ┌────────▼────────┐
               │  lib/api.ts │   │  auth/authApi.ts │
               │  apiFetch   │   │  refresh(rt)     │
               └─────────────┘   └─────────────────┘
                                         │
                               ┌─────────▼──────────┐
                               │  lib/authSession.ts │
                               │  getAccessToken     │
                               │  getRefreshToken    │
                               │  setTokens          │
                               │  clearTokens        │
                               └────────────────────┘
```

## Auth URL exclusion

The following path segments are excluded from auto-refresh (safety net; these functions already
use `apiFetch` directly in normal usage):

- `/auth/login`
- `/auth/refresh`
- `/auth/password`
- `/auth/oidc`
- `/auth/invites`
- `/auth/logout`

## Concurrency guard

A module-level `let inflightRefresh: Promise<TokenPair> | null` variable ensures that if multiple
concurrent requests all receive a 401, only one refresh call is made. All concurrent callers await
the same promise. The variable is reset to `null` in `.finally()` after the promise settles.

## Token storage

- Access and refresh tokens remain in `sessionStorage` using existing keys `nchat_at` / `nchat_rt`.
- No `localStorage`, no cookies, no URL.
- If backend rotates the refresh token, the new token is stored via `setTokens`.
- Tokens are never logged.

## Refresh trigger policy

- Refresh is **request-driven only** — triggered by a `401` response on an active API call.
- No background timer, no keepalive, no periodic polling.
- Backend remains authoritative for idle/absolute session expiry.

## Files

| File                                                | Action    |
| --------------------------------------------------- | --------- |
| `apps/web/src/lib/authClient.ts`                    | New       |
| `apps/web/src/lib/authClient.test.ts`               | New       |
| `docs/runbooks/task-frontend-auto-token-refresh.md` | New       |
| Existing source files                               | Unchanged |

## Tests

Unit tests (`authClient.test.ts`):

1. Attaches `Authorization: Bearer` header when access token is present
2. Omits `Authorization` header when no access token
3. Returns result directly on non-401 success
4. Re-throws non-401 errors without attempting refresh
5. 401 triggers refresh and retries original request once on success
6. Refresh success stores new tokens via `setTokens`
7. Retry uses new `Authorization` header
8. Refresh failure: `clearTokens()`, re-throw, no retry
9. No retry after failed refresh (request called exactly once)
10. No refresh token present: `clearTokens()`, no `refresh` call
11. Concurrent 401s trigger exactly one refresh call
12. Auth endpoint 401 does not trigger refresh (parameterized over all excluded paths)

E2E: Deferred — no authenticated endpoints exist in the app yet. Once the first
authenticated page feature is added, a Playwright smoke test should be added to
`e2e/auth.spec.ts` using Playwright route mocking for the refresh + retry cycle.

## Out of scope

- Backend refresh endpoint changes
- Persistent refresh across browser restarts
- Background keepalive timer
- OAuth/OIDC provider changes
- Mobile app behavior
