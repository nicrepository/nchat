# Frontend Route Protection

## Summary

This runbook documents the frontend route protection implementation added in the
`feat/web-route-protection` branch. It describes the guard mechanism, its scope,
and its explicit limitations.

---

## Important: UX Defense, Not Authorization Boundary

**Frontend route protection is UX defense only.**

The guards in this codebase (`RequireAuth`, `LoginPage` guest guard) prevent
unauthenticated users from seeing protected UI and provide a better user
experience. They are **not** a security boundary.

**Backend authorization remains mandatory.** Every API endpoint must enforce
authorization independently. A user who bypasses frontend guards — by disabling
JavaScript, modifying sessionStorage, or using devtools — must still be rejected
by the backend if they lack the required permissions.

Do not rely on frontend guards for data confidentiality or access control.

---

## Architecture

### `RequireAuth` component

Wraps any route that requires an authenticated session:

```tsx
<Route
  path="/"
  element={
    <RequireAuth>
      <HomePage />
    </RequireAuth>
  }
/>
```

Behavior:

- **Access token present** → renders children immediately.
- **Only refresh token present** → enters `"checking"` state, renders nothing,
  attempts a silent token refresh. On success, renders children. On failure,
  redirects to `/login`.
- **No tokens** → redirects to `/login` immediately without rendering children.
- **After mount:** subscribes to `onAuthChange` from `authSession`. If
  `clearTokens()` is called anywhere (e.g., logout, forced invalidation),
  `RequireAuth` re-evaluates and redirects to `/login`.
- **Location state:** passes `state: { from: location.pathname }` so post-login
  redirect can return the user to the originally-requested route.

### `LoginPage` guest guard

If `isAuthenticated()` is true when `LoginPage` renders, it immediately redirects
to the safe destination — `location.state.from` if it is a valid internal path,
or `/` otherwise. This applies only to `/login`; the other public auth routes
(`/forgot-password`, `/reset-password`, `/accept-invite`, `/oidc-callback`) are
not affected.

### `authSession` listener

`setTokens()` and `clearTokens()` call `notifyAuthChange()`, which invokes all
registered `() => void` callbacks. Callbacks receive **no arguments** — they
never carry token values. Callers read state via `isAuthenticated()`.

```typescript
const unsub = onAuthChange(() => {
  // read isAuthenticated() here — no token value in the callback
});
// call unsub() to unsubscribe
```

### `safeRedirect` helper

`isInternalPath(path)` and `safeFrom(from)` validate and sanitize redirect targets.
Safe internal paths must start with `/`, must not start with `//` (protocol-relative
URL), and must not start with `/\` (Windows-style variant). Any value that fails
the check silently falls back to `/`.

---

## Token Storage

Tokens are stored exclusively in `sessionStorage` via the `authSession` helper:

| Constraint                   | Status                                             |
| ---------------------------- | -------------------------------------------------- |
| `sessionStorage` only        | ✅ enforced in `authSession.ts`                    |
| No `localStorage`            | ✅ no `localStorage.*` calls                       |
| No cookies                   | ✅ no `document.cookie` usage                      |
| No tokens in URL             | ✅ `state.from` stores only pathname, never tokens |
| No token in listener payload | ✅ callbacks receive no arguments                  |

---

## Route Inventory

### Protected routes (require authenticated session)

| Path                  | Guard                        |
| --------------------- | ---------------------------- |
| `/`                   | `RequireAuth`                |
| `*` (catch-all → `/`) | `RequireAuth` (via redirect) |

### Public routes (no auth required)

| Path               | Notes                                          |
| ------------------ | ---------------------------------------------- |
| `/login`           | Guest guard redirects authenticated users away |
| `/forgot-password` | Always accessible                              |
| `/reset-password`  | Always accessible                              |
| `/accept-invite`   | Always accessible                              |
| `/oidc-callback`   | Always accessible; hardened in a prior PR      |

---

## Out of Scope

The following are **not** implemented and must not be assumed:

- RBAC or role-based route permissions
- Admin route permissions matrix
- Backend authorization changes
- Cookie-based sessions
- Background keepalive timer
- Cross-tab session synchronization
- Mobile route protection
- Provider selector UI

---

## Validation Commands

```bash
pnpm format:check:web
pnpm lint:web
pnpm typecheck:web
pnpm test:web
pnpm test:coverage:web
pnpm format:check:docs
git diff --check origin/develop...HEAD
semgrep scan --config p/secrets --config p/owasp-top-ten apps/web docs README.md
pnpm test:e2e:web   # requires Playwright/Chromium; runs in CI if unavailable locally
```
