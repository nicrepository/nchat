# Runbook: Frontend Auth Entry Flows

**RF traceability:** RF-46 (invites), RF-48 (password recovery), auth MVP login
**Branch:** `feat/web-auth-entry-flows`
**Plan used:** `docs/superpowers/plans/2026-06-02-frontend-auth-entry-flows.md`
**Visual reference only:** `prototype/claude-design-v1/nic-chat/login.html`, `tokens.css`, and `assets/`

The prototype is a static visual reference only. Production code is implemented in React/TypeScript/CSS under `apps/web`; raw prototype HTML/JS is not copied into the app.

## Routes implemented

| Route                                          | Component                   | Backend endpoint             |
| ---------------------------------------------- | --------------------------- | ---------------------------- |
| `/login`                                       | `LoginPage`                 | `POST /auth/login`           |
| `/forgot-password`                             | `ForgotPasswordPage`        | `POST /auth/password/forgot` |
| `/reset-password` with query parameter `token` | `ResetPasswordPage`         | `POST /auth/password/reset`  |
| `/accept-invite` with query parameter `token`  | `AcceptInvitePage`          | `POST /auth/invites/accept`  |
| protected app routes                           | `RequireAuth` -> `HomePage` | `POST /auth/refresh`         |

`apps/web` defaults `VITE_AUTH_API_BASE_URL` to `/api/auth`, so all auth calls are same-origin gateway requests to `/api/auth/*`. **Same-origin is the only supported mode in this MVP.**

> **Cross-origin is not supported.** `apiFetch` does not set `credentials: "include"`, so the
> `nchat_rt` HttpOnly cookie is **not transmitted** on cross-origin requests. Setting
> `VITE_AUTH_API_BASE_URL=http://localhost:8081/auth` in `.env.local` would silently break token
> refresh and logout while appearing to work for login. Use the gateway (`/api/auth`) for all
> development and testing.

## Token storage decision

The access token (`nchat_at`) is kept in `sessionStorage`; the refresh token (`nchat_rt`) is stored exclusively in an `HttpOnly; Secure; SameSite=Strict` cookie set by the backend.

- No `localStorage` is used.
- The access token is scoped to the current browser tab and is cleared when the tab closes.
- The refresh token cookie is inaccessible to JavaScript, limiting the blast radius of an XSS exploit to the current access-token lifetime.
- On page load, `RequireAuth` has no access token; it issues `POST /auth/refresh` so the browser sends the cookie automatically. If refresh fails (no cookie or revoked), the user is redirected to `/login`.

## Security notes

- Login failures render one generic message and do not reveal account status, lockout, or credential specifics.
- Forgot-password always renders the same generic success state, even when the API fails, to avoid email enumeration.
- Reset and invite tokens are read from the URL query string, cleared from router history, sent only in the request body, and not stored in browser storage.
- Reset and invite tokens arrive by query string in this PR because backend email templates currently generate query links. The frontend immediately removes the token from browser history with replace navigation, but server/proxy access logs may still see the initial URL. Future hardening: migrate email links to URL fragments carrying the token parameter or add gateway log redaction.
- Backend error messages are not rendered directly. Password-policy and token failures are mapped to fixed UI copy.
- No frontend code logs passwords, access tokens, refresh tokens, reset tokens, invite tokens, or auth headers.
- Invite acceptance does not auto-login because the backend returns user data, not tokens.
- The SSO button is disabled because no supported frontend OIDC endpoint exists in this scope.
- All auth calls are same-origin (`/api/auth`). Cross-origin is not supported in this MVP; see the `VITE_AUTH_API_BASE_URL` note above.

## Local commands

```bash
pnpm lint:web
pnpm typecheck:web
pnpm test:web
pnpm test:coverage:web
pnpm build:web
```

Run locally:

```bash
make dev-web
```

With local gateway enabled, the web app is available through Traefik at `http://nchat.local:8080` and auth calls route to `/api/auth/*`.

## Out of scope

- Backend auth behavior changes
- Database migrations
- Full SSO/OIDC flow
- Admin UI
- Final RBAC
- Device/session UI
- MFA/CAPTCHA
- Auto-login after reset or invite acceptance
