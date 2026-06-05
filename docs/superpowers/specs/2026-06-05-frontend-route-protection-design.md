# Design: Frontend Route Protection

**Date:** 2026-06-05  
**Branch:** feat/web-route-protection  
**Base:** develop  
**PR target:** develop

---

## Overview

Harden frontend route protection using the existing `sessionStorage` auth flow
and the established `authSession` helper. No new context providers, no RBAC, no
backend changes. The goal is correct UX defense: an unauthenticated user cannot
see protected content, and `RequireAuth` reacts to session invalidation after
initial mount.

**Frontend route protection is UX defense, not an authorization boundary.**
Backend authorization remains mandatory; the frontend guard only prevents
unnecessary rendering of protected pages.

---

## Scope

### In scope

- `authSession.ts` — listener mechanism (`onAuthChange`, `notifyAuthChange`)
- `RequireAuth.tsx` — reactivity to `clearTokens()` after mount + location state preservation
- `LoginPage.tsx` — guest guard + safe `state.from` post-login redirect
- Unit tests for all new behaviors
- E2E smoke tests (Playwright, mocked backend)
- `docs/runbooks/task-frontend-route-protection.md`

### Explicitly out of scope

- React Context / AuthProvider
- OIDC callback (already hardened)
- `authClient.ts` (no changes)
- Backend authorization / RBAC
- Cross-tab sync (sessionStorage is tab-scoped; not useful enough to justify)
- Admin route permissions matrix
- Provider selector UI
- Cookie-based sessions
- Background keepalive timer
- Mobile route protection

---

## Architecture

### 1. `authSession.ts` — listener mechanism

Add a private `Set<() => void>` of listeners. Add one internal function
`notifyAuthChange()` called by `setTokens()` and `clearTokens()`. Export a
public registration function that returns an unsubscribe function.

```
onAuthChange(listener: () => void): () => void
```

The callback receives **no arguments** — it never carries tokens or auth state.
Callers read state themselves via `isAuthenticated()`, `getAccessToken()`, etc.

```
setTokens()   → stores tokens → notifyAuthChange()
clearTokens() → removes tokens → notifyAuthChange()
```

No cross-tab support. No `window.addEventListener("storage", ...)`.

### 2. `RequireAuth.tsx` — reactive guard

**Init:** calls `isAuthenticated()` synchronously. If only a refresh token is
present, state is `"checking"` and a token refresh is attempted (existing logic,
unchanged).

**After mount:** subscribes to `onAuthChange` in a `useEffect`. When
`clearTokens()` is called anywhere, the listener fires, `isAuthenticated()`
returns `false`, and state is set to `"unauthenticated"` → redirect to `/login`.

**Location state:** when redirecting, pass `state: { from: location.pathname }`
using `useLocation()`. Only the pathname is stored — no origin, no query params
from the current URL that could carry external data.

**Content hiding:** `if (authState === "checking") return null` is preserved
(existing behavior). Protected content is never rendered before auth resolves.

### 3. `LoginPage.tsx` — guest guard + safe redirect

**Guest guard:** on render, if `isAuthenticated()` is true, immediately navigate
to the safe destination (see below). This prevents a logged-in user from seeing
the login form.

**Safe destination resolution:**

```
const from = location.state?.from;
const dest = isInternalPath(from) ? from : "/";
```

`isInternalPath(s)` checks:

- `s` is a non-empty string
- starts with `/`
- does not start with `//` (protocol-relative URL)
- does not contain `:` before the first `/` (no `http:` etc.)

Any value that fails this check falls back to `/`. External redirects are
rejected silently.

**Post-login redirect:** after a successful `login()` call (and OIDC exchange in
`OIDCCallbackPage`), navigate to the same safe destination.

---

## Data flow

```
Unauthenticated visit to /
  → RequireAuth: isAuthenticated() = false, state = "unauthenticated"
  → <Navigate to="/login" state={{ from: "/" }} replace />

User logs in via LoginPage
  → setTokens(at, rt)         ← notifyAuthChange fires (no-op, RequireAuth not mounted here)
  → navigate(dest, replace)   ← where dest = state.from ?? "/"

After mount: clearTokens() called (e.g. logout, forced invalidation)
  → notifyAuthChange()
  → RequireAuth listener: isAuthenticated() = false
  → setAuthState("unauthenticated")
  → <Navigate to="/login" replace />
```

---

## Security constraints

| Constraint                      | Implementation                                                                       |
| ------------------------------- | ------------------------------------------------------------------------------------ |
| Tokens never in URL             | `setTokens`/`clearTokens` write only to `sessionStorage`; listeners carry no payload |
| No localStorage                 | Only `sessionStorage.*` calls in `authSession.ts`                                    |
| No cookies                      | No `document.cookie` usage                                                           |
| No external redirect            | `isInternalPath()` rejects any non-internal path                                     |
| No query-param redirect         | `from` reads only `location.state.from`, never `searchParams`                        |
| Tokens not logged               | No `console.log` with token values                                                   |
| Content not exposed before auth | `if (authState === "checking") return null`                                          |

---

## Tests

### Unit — `authSession.test.ts` additions

- `onAuthChange` callback fires after `setTokens()`
- `onAuthChange` callback fires after `clearTokens()`
- unsubscribe (returned function) stops future calls
- multiple listeners all receive the call
- listener receives no arguments (payload is empty)

### Unit — `RequireAuth.test.tsx` additions

- protected route without tokens → redirects to `/login`, content not rendered
- authenticated → renders children
- `clearTokens()` after mount → redirects to `/login`
- `<Navigate>` includes `state.from = location.pathname` when redirecting

### Unit — `LoginPage.test.tsx` additions

- already-authenticated user → redirects away immediately
- safe `state.from` internal path → used as post-login destination
- `state.from` with external URL → rejected, navigate to `/`
- `state.from` with protocol-relative URL `//evil.com` → rejected

### E2E — `auth.spec.ts` additions

- unauthenticated visit to `/` → redirects to `/login` (already exists, keep)
- authenticated user → accesses `/` without redirect
- `clearTokens()` after login → next navigation blocked
- login page with active session → redirects to `/`
- public routes (`/forgot-password`, `/reset-password`, `/accept-invite`) → accessible unauthenticated

---

## Docs

New file: `docs/runbooks/task-frontend-route-protection.md`

Must include:

- Frontend route protection is UX defense, not authorization boundary
- Backend authorization remains mandatory
- Tokens stored in existing `sessionStorage` helper only
- No localStorage, no cookies, no tokens in URL
- `RequireAuth` reacts to `setTokens`/`clearTokens` via `onAuthChange`

---

## Validation

```bash
pnpm format:check:web
pnpm lint:web
pnpm typecheck:web
pnpm test:web
pnpm test:coverage:web
pnpm format:check:docs
git diff --check origin/develop...HEAD
semgrep scan --config p/secrets --config p/owasp-top-ten apps/web docs README.md
pnpm test:e2e:web   # if Playwright/Chromium available in environment
```

---

## Commit message

```
feat(web): harden route protection
```
