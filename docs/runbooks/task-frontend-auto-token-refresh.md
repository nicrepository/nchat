# Runbook: Frontend Automatic Token Refresh

**Branch:** `feat/web-auto-token-refresh`
**Spec:** `docs/superpowers/specs/2026-06-05-web-auto-token-refresh-design.md`

## Overview

This runbook documents the automatic access-token refresh mechanism added to the web frontend in
`apps/web/src/lib/authClient.ts`. The implementation is fully request-driven: the access token is
refreshed only when an authenticated API call receives a `401 Unauthorized` response.

## Key principles

- **Access token refresh is request-driven, not timer-driven.** No background interval, keepalive,
  or periodic polling is used. The refresh is triggered only when an active API request receives
  a `401` response due to an expired access token.
- **Backend remains authoritative for idle and absolute session expiry.** The frontend does not
  attempt to keep a session alive by proactively refreshing tokens. If the backend decides the
  session has expired (idle timeout, absolute max age, revocation), the next API call will receive
  `401`, the refresh attempt will fail, and the user will be redirected to `/login`.
- **Refresh failure clears stored tokens and returns an unauthenticated error to the caller.**
  On refresh failure, `clearTokens()` is called (subject to the session-binding guard described
  below) and the original `401` error is re-thrown. Protected routes and callers are responsible
  for handling the unauthenticated state using the existing unauthenticated flow (e.g. `RequireAuth`
  redirecting to `/login` on the next render cycle after detecting no tokens).
- **No tokens in URL, localStorage, or cookies.** Access and refresh tokens are kept exclusively in
  `sessionStorage` using the existing keys `nchat_at` (access token) and `nchat_rt` (refresh
  token). This means tokens are scoped to the current browser tab and cleared when the tab closes.

## How it works

```
1. Authenticated page component calls authenticatedFetch("/api/some-resource", { method: "GET" })
2. authenticatedFetch injects Authorization: Bearer <nchat_at>
3. apiFetch sends the request
   a. 200 → return result
   b. 401 → attempt refresh:
      i.  Read nchat_rt from sessionStorage
      ii. If no refresh token → clearTokens(), rethrow 401
      iii. Call authApi.refresh(nchat_rt) [shared single-flight promise]
           - Refresh success → setTokens(new_at, new_rt) → retry original request once
           - Refresh failure → clearTokens(), rethrow 401
```

### Concurrency guard

A module-level `inflightRefresh` promise is shared across concurrent requests. If multiple API
calls receive `401` simultaneously, only one `POST /api/auth/refresh` call is made. All concurrent
callers await the same promise. The guard is reset to `null` in `.finally()` after settling.

### Late-arrival guard

The access token used for the original request is captured before the call. If the stored access
token has already changed by the time a `401` is caught (because a concurrent request completed a
refresh first), the request retries immediately with the newer token instead of triggering another
refresh call.

### Session-binding guard

`setTokens` and `clearTokens` are conditional on the stored refresh token still matching the one
that initiated the refresh. If the session changed while the refresh was in flight (e.g. the user
logged out or a newer login occurred), the in-flight refresh result is discarded and the current
session is preserved.

### Auth endpoint exclusion

The following URL pathnames never trigger auto-refresh. These functions call `apiFetch`
directly in normal usage; the exclusion is a safety net against accidental use and prevents false
positives from query-string values that contain auth path segments:

| Pathname prefix      | Auth function                         |
| -------------------- | ------------------------------------- |
| `/api/auth/login`    | `login()`                             |
| `/api/auth/refresh`  | `refresh()`                           |
| `/api/auth/password` | `forgotPassword()`, `resetPassword()` |
| `/api/auth/oidc`     | `oidcExchange()`                      |
| `/api/auth/invites`  | `acceptInvite()`                      |
| `/api/auth/logout`   | `logout()`                            |

URL detection uses `new URL(url, window.location.origin).pathname` to compare pathnames only,
preventing false positives from query-string values like `?next=/api/auth/login`.

### Body reuse

`authenticatedFetch` re-uses `init.body` for the retry. This is safe for JSON string bodies
(the only body type currently used). `ReadableStream` bodies are not supported.

## Usage

```typescript
import { authenticatedFetch } from "../lib/authClient";

// In a page or hook that makes authenticated API calls:
const data = await authenticatedFetch<MyResponse>("/api/some-endpoint", {
  method: "GET",
});
```

Do **not** use `authenticatedFetch` for auth endpoints (`/api/auth/*`). Those functions in
`authApi.ts` use `apiFetch` directly by design.

## Token storage

| Key        | Storage          | Scope       | Content                      |
| ---------- | ---------------- | ----------- | ---------------------------- |
| `nchat_at` | `sessionStorage` | Current tab | Access token (JWT or opaque) |
| `nchat_rt` | `sessionStorage` | Current tab | Refresh token                |

Both tokens are cleared by `clearTokens()` on refresh failure or explicit logout.

## E2E testing note

The Playwright smoke test for the `authenticatedFetch` refresh-and-retry cycle is **deferred**
until the first authenticated page endpoint exists in the app. Once such an endpoint is added,
add a test to `apps/web/e2e/auth.spec.ts` that:

1. Logs in (mock backend).
2. Seeds an expired `nchat_at` and a valid `nchat_rt` in `sessionStorage`.
3. Mocks `GET /api/<endpoint>` to return `401` on first call, `200` on retry.
4. Mocks `POST /api/auth/refresh` to return new tokens.
5. Navigates to the page and verifies the content loads — confirming the refresh cycle was
   transparent to the user.

If the Playwright environment is unavailable (e.g., Chromium not installed on CI runner), rely on
the unit tests in `authClient.test.ts` for coverage of this behaviour.

## Validation commands

```bash
pnpm lint:web
pnpm typecheck:web
pnpm test:web
pnpm test:coverage:web
pnpm format:check:web
pnpm format:check:docs
```

## Out of scope

- Backend refresh endpoint changes
- Refresh via cookies
- Persistent refresh across browser restarts (would require `localStorage` or `httpOnly` cookie)
- Background keepalive timer
- OAuth/OIDC provider changes
- Mobile app behaviour
- Admin session policy UI
