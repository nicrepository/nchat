# Runbook: Frontend Auth Entry Flows

**RF traceability:** RF-46 (invites), RF-48 (password recovery), auth MVP login
**Branch:** `feat/web-auth-entry-flows`
**Plan used:** `docs/superpowers/plans/2026-06-02-frontend-auth-entry-flows.md`
**Visual reference only:** `prototype/claude-design-v1/nic-chat/login.html`, `tokens.css`, and `assets/`

The prototype is a static visual reference only. Production code is implemented in React/TypeScript/CSS under `apps/web`; raw prototype HTML/JS is not copied into the app.

## Routes implemented

| Route                       | Component                   | Backend endpoint             |
| --------------------------- | --------------------------- | ---------------------------- |
| `/login`                    | `LoginPage`                 | `POST /auth/login`           |
| `/forgot-password`          | `ForgotPasswordPage`        | `POST /auth/password/forgot` |
| `/reset-password?token=...` | `ResetPasswordPage`         | `POST /auth/password/reset`  |
| `/accept-invite?token=...`  | `AcceptInvitePage`          | `POST /auth/invites/accept`  |
| protected app routes        | `RequireAuth` -> `HomePage` | `POST /auth/refresh`         |

`apps/web` defaults `VITE_AUTH_API_BASE_URL` to `/api/auth`, so same-origin gateway calls are sent as `/api/auth/*`. For direct auth-service development, override the base URL in `.env.local`:

```env
VITE_AUTH_API_BASE_URL=http://localhost:8081/auth
```

## Token storage decision

The frontend stores access and refresh tokens in `sessionStorage` using keys `nchat_at` and `nchat_rt`.

- No `localStorage` is used.
- `sessionStorage` is scoped to the current browser tab and is cleared by the browser when the tab closes.
- `RequireAuth` allows a page reload when only the refresh token is present, attempts `POST /auth/refresh`, and redirects to `/login` if refresh fails.
- If persistent sessions are needed later, prefer an `httpOnly` refresh-token cookie with backend coordination.

## Security notes

- Login failures render one generic message and do not reveal account status, lockout, or credential specifics.
- Forgot-password always renders the same generic success state, even when the API fails, to avoid email enumeration.
- Reset and invite tokens are read from the URL query string, cleared from router history, sent only in the request body, and not stored in browser storage.
- Backend error messages are not rendered directly. Password-policy and token failures are mapped to fixed UI copy.
- No frontend code logs passwords, access tokens, refresh tokens, reset tokens, invite tokens, or auth headers.
- Invite acceptance does not auto-login because the backend returns user data, not tokens.
- The SSO button is disabled because no supported frontend OIDC endpoint exists in this scope.

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
