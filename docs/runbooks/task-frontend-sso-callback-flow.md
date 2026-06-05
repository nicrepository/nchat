# Runbook: Frontend SSO Button and Callback Flow

**RF traceability:** Keycloak OIDC provider (feat/auth-keycloak-oidc-provider)
**Branch:** `feat/web-sso-button-callback-flow`
**Depends on runbook:** `task-auth-oidc-keycloak.md`, `task-frontend-auth-entry-flows.md`

## Overview

This runbook documents the frontend SSO entry flow that integrates with the existing backend
Keycloak OIDC provider. The backend owns the OIDC redirect and callback endpoints; the frontend
only adds the entry point (SSO button) and the exchange landing page.

## How the flow works

```
LoginPage  →  browser navigation  →  GET /auth/oidc/keycloak/login  (backend redirect)
                                           ↓
                               Keycloak authorization page
                                           ↓
                               GET /auth/oidc/keycloak/callback  (backend validates)
                                           ↓
                               redirect → /oidc-callback?code=<opaque>
                                           ↓
OIDCCallbackPage  →  POST /auth/oidc/keycloak/exchange  →  NChat access + refresh tokens
                                           ↓
                               sessionStorage  →  navigate /
```

**Key points:**

- The backend validates the provider session and issues a **one-time opaque exchange code**.
- The frontend exchanges that code via `POST /auth/oidc/keycloak/exchange`; **no real auth
  tokens ever appear in the URL**.
- The exchange code is consumed immediately and removed from the browser URL via
  `window.history.replaceState` before navigation, so it does not appear in the browser
  history or referrer headers.
- Tokens are stored in `sessionStorage` (keys `nchat_at`, `nchat_rt`), consistent with the
  email/password login flow. See `task-frontend-auth-entry-flows.md` for storage rationale.

## Frontend components

| File                                     | Role                                                                                                 |
| ---------------------------------------- | ---------------------------------------------------------------------------------------------------- |
| `apps/web/src/auth/LoginPage.tsx`        | Renders the "Entrar com Keycloak" `<a>` link that starts browser navigation to the backend login URL |
| `apps/web/src/auth/OIDCCallbackPage.tsx` | Reads `?code`, calls `POST /auth/oidc/keycloak/exchange`, stores tokens, navigates to `/`            |
| `apps/web/src/auth/authApi.ts`           | `oidcLoginUrl()` builds the backend login URL; `oidcExchange(code)` calls the exchange endpoint      |
| `apps/web/src/App.tsx`                   | Registers the `/oidc-callback` route                                                                 |

## Backend endpoints (owned by auth-service)

| Method | Path                           | Description                                                        |
| ------ | ------------------------------ | ------------------------------------------------------------------ |
| `GET`  | `/auth/oidc/keycloak/login`    | Initiates the OIDC redirect to Keycloak; not called with fetch     |
| `GET`  | `/auth/oidc/keycloak/callback` | Backend-only; validates the OIDC callback from Keycloak            |
| `POST` | `/auth/oidc/keycloak/exchange` | Exchanges the opaque one-time code for NChat access/refresh tokens |

## SSO button design

The SSO button in `LoginPage` is an `<a>` anchor tag that triggers a full browser navigation
to the backend login URL. It is intentionally **not** an API `fetch` call — the OIDC redirect
flow requires a real browser redirect so that the Keycloak authorization server can set its own
session cookies and return to the configured redirect URI.

Button label: **"Entrar com Keycloak"**

The button targets the **Keycloak** provider. Azure AD, Google Workspace, and SambaAD provider
UI selection is **out of scope** for this implementation.

## Error handling

All SSO errors surface as a single generic message:

> "Não foi possível concluir o login com SSO."

Raw backend error details, error codes, and the exchange code value are never rendered in the UI,
written to `console`, or included in tests.

## Security constraints

- Access tokens and refresh tokens are **never placed in URLs**.
- `localStorage` is not used.
- The exchange code is removed from the browser URL before navigation.
- No arbitrary `redirect_after` query parameter is accepted.
- No open redirect: the callback page always navigates to the hard-coded `/` path on success.

## Config

The auth API base URL is read from the `VITE_AUTH_API_BASE_URL` env variable, defaulting to
`/api/auth` for same-origin gateway calls. Override in `.env.local` for direct auth-service
development:

```env
VITE_AUTH_API_BASE_URL=http://localhost:8081/auth
```

## Out of scope

- Azure AD UI option
- Google Workspace UI option
- SambaAD login UI
- Provider selector UI
- Admin provider configuration UI
- Backend OIDC changes

## Local commands

```bash
pnpm lint:web
pnpm typecheck:web
pnpm test:web
pnpm test:coverage:web
pnpm format:check:web
pnpm format:check:docs
# E2E (requires Playwright browsers installed):
pnpm test:e2e:web
```
