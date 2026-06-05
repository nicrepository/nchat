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

URL detection uses `new URL(url, window.location.origin).pathname` to compare pathnames only,
preventing false positives from query-string values that contain auth path segments
(e.g. `/api/search?next=/api/auth/login`). Detection uses exact-match or prefix+`/`-boundary
check (`pathname === prefix || pathname.startsWith(prefix + "/")`) so paths like
`/api/auth/loginExtra` do **not** match `/api/auth/login`. The following pathname prefixes are
excluded:

- `/api/auth/login`
- `/api/auth/refresh`
- `/api/auth/password`
- `/api/auth/oidc`
- `/api/auth/invites`
- `/api/auth/logout`

## Concurrency guard

A module-level `inflightRefresh: { refreshToken: string; promise: Promise<TokenPair> } | null`
ensures that concurrent 401s from the **same session generation** share one refresh call. The
refresh token is stored alongside the promise. Only requests whose current refresh token matches
the in-flight record share it; cross-session requests (different refresh token) each create their
own independent refresh call and cannot observe or modify each other's in-flight state. The
variable is reset to `null` after the promise settles and any token commit is complete.

## Late-arrival guard

The access token used for the original request is captured before the first `apiFetch` call. If
the stored access token has already changed by the time a `401` is caught (because a concurrent
request completed a refresh first), the request retries immediately with the newer token — no
second refresh is triggered.

## Session-binding guard

`setTokens` and `clearTokens` are conditional on the stored refresh token after the promise
settles:

- **First settler** (RT unchanged): calls `setTokens`, then retries.
- **Concurrent waiter** (RT equals `newTokens.refreshToken` committed by first settler): skips
  `setTokens`, falls through to retry with current access token.
- **External session change** (logout or newer login while in flight): neither `setTokens` nor
  `clearTokens` is called; the original `401` is re-thrown without retrying under the new or
  absent session.

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

1. Attaches `authorization` header when access token is present
2. Omits `authorization` header when no access token
3. Handles plain object, Headers instance, and tuple-array HeadersInit forms
4. Does not mutate a caller-provided Headers object
5. Returns result directly on non-401 success
6. Re-throws non-401 errors without attempting refresh
7. 401 triggers refresh and retries original request once on success
8. Refresh success stores new tokens via `setTokens`
9. Retry uses updated `authorization` header
10. Refresh failure: `clearTokens()`, re-throw, no retry
11. No retry after failed refresh (request called exactly once)
12. No refresh token present: `clearTokens()`, no `refresh` call
13. Concurrent 401s (same session) trigger exactly one refresh call
14. Concurrent waiters on the same refresh both retry once with the new access token
15. Late-arrival guard: retries with newer token, no second refresh
16. Late-arrival guard: retry uses newer access token in header
17. Late second 401 after first refresh settles: retries with newer token, no extra refresh
18. Stale refresh success after `clearTokens` does not restore tokens
19. Stale refresh success after newer `setTokens` does not overwrite newer tokens and rethrows original 401
20. Stale refresh failure after newer `setTokens` does not clear newer tokens
21. Cross-session 401 with different refresh token starts its own refresh call
22. Auth endpoint 401 does not trigger refresh (parameterized over all excluded paths)
23. Query-string auth path does not skip refresh
24. Path that merely starts with auth prefix (no `/` boundary) does not skip refresh

E2E: Deferred — no authenticated endpoints exist in the app yet. Once the first
authenticated page feature is added, a Playwright smoke test should be added to
`e2e/auth.spec.ts` using Playwright route mocking for the refresh + retry cycle.

## Out of scope

- Backend refresh endpoint changes
- Refresh via cookies
- Persistent refresh across browser restarts
- Background keepalive timer
- OAuth/OIDC provider changes
- Mobile app behavior
- Admin session policy UI
