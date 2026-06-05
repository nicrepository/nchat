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
- **Refresh failure clears the session and returns the user to login.** On refresh failure,
  `clearTokens()` is called, the original `401` error is re-thrown, and `RequireAuth` or the
  calling page is expected to redirect the user to `/login`.
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

### Auth endpoint exclusion

The following URL path segments never trigger auto-refresh. These functions call `apiFetch`
directly in normal usage; the exclusion is a safety net:

| Path             | Auth function                         |
| ---------------- | ------------------------------------- |
| `/auth/login`    | `login()`                             |
| `/auth/refresh`  | `refresh()`                           |
| `/auth/password` | `forgotPassword()`, `resetPassword()` |
| `/auth/oidc`     | `oidcExchange()`                      |
| `/auth/invites`  | `acceptInvite()`                      |
| `/auth/logout`   | `logout()`                            |

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
- Persistent refresh across browser restarts (would require `localStorage` or `httpOnly` cookie)
- Background keepalive timer
- OAuth/OIDC provider changes
- Mobile app behaviour
- Admin session policy UI
