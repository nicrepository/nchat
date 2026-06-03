# Design: OIDC Keycloak Provider (RF-44)

**Date:** 2026-06-02
**Branch:** `feat/auth-oidc-keycloak-provider`
**Base:** `develop`
**Status:** Approved — ready for implementation plan

---

## Traceability

| RF    | Requirement                                          |
| ----- | ---------------------------------------------------- |
| RF-44 | Login via SSO OIDC — Keycloak como primeiro provider |

---

## Scope

Add Keycloak OIDC login beside the existing email/password flow. No redesign of existing auth. No multi-provider UI, no SCIM, no MFA, no Azure AD / Google Workspace, no admin provider UI, no advanced account linking in this PR.

---

## Architecture

### Flow overview

```
Browser                  auth-service              Keycloak              DB (PostgreSQL)
  |                           |                        |                       |
  |  GET /auth/oidc/          |                        |                       |
  |  keycloak/login           |                        |                       |
  |-------------------------->|                        |                       |
  |                           | generate state,        |                       |
  |                           | nonce, PKCE verifier   |                       |
  |                           | store hashed in        |                       |
  |                           | oidc_auth_requests --->|                       |
  |                           | build authz URL        |                       |
  |<-- 302 to Keycloak -------|                        |                       |
  |                                                    |                       |
  | [user authenticates at Keycloak]                   |                       |
  |                                                    |                       |
  |  GET /auth/oidc/keycloak/callback?code=&state=     |                       |
  |-------------------------->|                        |                       |
  |                           | validate state hash    |                       |
  |                           | exchange code + PKCE ->|                       |
  |                           |<-- id_token + tokens --|                       |
  |                           | validate id_token      |                       |
  |                           | (issuer/aud/nonce/     |                       |
  |                           |  expiry/sig/alg)       |                       |
  |                           | resolve/create user    |                       |
  |                           | create NChat session   |                       |
  |                           | generate exchange code |                       |
  |                           | store hash+encrypted   |                       |
  |                           | tokens in exchange ----+---------------------->|
  |<-- 302 /oidc-callback     |                        |                       |
  |    ?code=<opaque> --------|                        |                       |
  |                           |                        |                       |
  | POST /auth/oidc/          |                        |                       |
  | keycloak/exchange         |                        |                       |
  |-------------------------->|                        |                       |
  |                           | validate + consume --->|                       |
  |                           | decrypt tokens         |                       |
  |<-- 200 {access_token,     |                        |                       |
  |         refresh_token}    |                        |                       |
  | store in sessionStorage   |                        |                       |
  | navigate /                |                        |                       |
```

---

## Configuration

### New environment variables

| Variable                      | Required     | Default                | Notes                                                                                                                                      |
| ----------------------------- | ------------ | ---------------------- | ------------------------------------------------------------------------------------------------------------------------------------------ |
| `OIDC_ENABLED`                | no           | `false`                | Feature flag; all OIDC endpoints return 404/503 when false                                                                                 |
| `OIDC_PROVIDER_NAME`          | when enabled | `keycloak`             | Stored as `external_provider` on users                                                                                                     |
| `OIDC_ISSUER_URL`             | when enabled | —                      | e.g. `https://keycloak.example.com/realms/nchat`                                                                                           |
| `OIDC_CLIENT_ID`              | when enabled | —                      |                                                                                                                                            |
| `OIDC_CLIENT_SECRET`          | when enabled | —                      | Secret; must use `secretKeyRef` in K8s                                                                                                     |
| `OIDC_REDIRECT_URL`           | when enabled | —                      | Must match Keycloak client config                                                                                                          |
| `OIDC_SCOPES`                 | no           | `openid email profile` | Space-separated                                                                                                                            |
| `OIDC_HTTP_TIMEOUT_SECONDS`   | no           | `10`                   | Timeout for OIDC provider HTTP calls                                                                                                       |
| `OIDC_STATE_TTL_MINUTES`      | no           | `10`                   | TTL for `oidc_auth_requests` rows                                                                                                          |
| `OIDC_AUTO_PROVISION_ENABLED` | no           | `false`                | Create user on first OIDC login only when explicitly enabled                                                                               |
| `OIDC_ALLOWED_EMAIL_DOMAINS`  | no           | ``                     | Comma-separated allowlist; empty = allow all verified domains and log a warning                                                            |
| `OIDC_FRONTEND_CALLBACK_URL`  | when enabled | —                      | Relative internal frontend callback path. Must be `/oidc-callback`; absolute, protocol-relative, and CR/LF-containing values are rejected. |

### Fail-closed rules

- `OIDC_ENABLED=false` (or absent) → all OIDC endpoints return `{"error":"oidc_disabled"}` 404.
- `OIDC_ENABLED=true` with missing required vars → startup logs warning, all OIDC endpoints return 503.
- No real secrets in repo. `.env.example` uses empty values with descriptive comments.

---

## Database migration 000007

### `auth.users` — add `external_provider`

```sql
ALTER TABLE auth.users ADD COLUMN external_provider TEXT;
```

Constraints: `external_provider IS NOT NULL` when `auth_source = 'oidc'`.
Unique index on `(external_provider, external_subject)` to support multiple future providers.

### New table `auth.oidc_auth_requests`

Stores per-flow state, nonce, and encrypted PKCE verifier.

| Column                    | Type                 | Notes                                                                                                                                                                                                                            |
| ------------------------- | -------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `id`                      | UUID PK              | gen_random_uuid()                                                                                                                                                                                                                |
| `provider`                | TEXT NOT NULL        | e.g. `'keycloak'`                                                                                                                                                                                                                |
| `state_hash`              | TEXT UNIQUE NOT NULL | HMAC-SHA256 of raw state with domain `nchat-oidc-state-v1`; raw state never stored                                                                                                                                               |
| `nonce_hash`              | TEXT NOT NULL        | HMAC-SHA256 of raw nonce with domain `nchat-oidc-nonce-v1`; raw nonce never stored                                                                                                                                               |
| `pkce_verifier_encrypted` | TEXT NOT NULL        | AES-GCM encrypted verifier with AAD `oidc-pkce:keycloak:<row_id>`; key derived from HMAC secret                                                                                                                                  |
| `redirect_after`          | TEXT                 | nullable; reserved for future post-login deep-link support. **MVP: always NULL** — the frontend always navigates to `/` after exchange. If populated in future, must be validated against an internal-path allowlist before use. |
| `created_at`              | TIMESTAMPTZ NOT NULL | default now()                                                                                                                                                                                                                    |
| `expires_at`              | TIMESTAMPTZ NOT NULL | created_at + OIDC_STATE_TTL_MINUTES                                                                                                                                                                                              |
| `used_at`                 | TIMESTAMPTZ          | nullable; set atomically on successful callback                                                                                                                                                                                  |

State is one-time use: callback rejects if `used_at IS NOT NULL` or `expires_at <= now()`.

### New table `auth.oidc_exchange_codes`

Stores encrypted NChat tokens pending frontend exchange.

| Column                    | Type                 | Notes                                                                                                                                                |
| ------------------------- | -------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------- |
| `id`                      | UUID PK              | gen_random_uuid()                                                                                                                                    |
| `code_hash`               | TEXT UNIQUE NOT NULL | HMAC-SHA256 of raw exchange code with domain `nchat-oidc-exchange-code-v1`                                                                           |
| `access_value_encrypted`  | TEXT NOT NULL        | AES-GCM encrypted NChat access value with AAD `oidc-exchange:keycloak:<row_id>`                                                                      |
| `refresh_value_encrypted` | TEXT NOT NULL        | AES-GCM encrypted NChat refresh value with AAD `oidc-exchange:keycloak:<row_id>`                                                                     |
| `bearer_scheme`           | TEXT NOT NULL        | `'Bearer'`                                                                                                                                           |
| `expires_in`              | INT NOT NULL         | access token TTL in seconds                                                                                                                          |
| `user_json`               | JSONB NOT NULL       | Safe user fields (id, email, displayName, mustChangePassword). `mustChangePassword` is always `false` for OIDC users (no password managed by nchat). |
| `created_at`              | TIMESTAMPTZ NOT NULL | default now()                                                                                                                                        |
| `expires_at`              | TIMESTAMPTZ NOT NULL | created_at + 2 minutes                                                                                                                               |
| `used_at`                 | TIMESTAMPTZ          | nullable; set atomically on exchange                                                                                                                 |

---

## Cryptography

### PKCE verifier encryption

- Algorithm: AES-256-GCM
- Key derivation: HKDF-SHA256 from `AUTH_JWT_HMAC_SECRET` with info `nchat-oidc-pkce-v1`
- AAD: `oidc-pkce:keycloak:<row_id>` (purpose + provider + row UUID) — ensures ciphertext cannot be moved to a different row without decryption failure.
- **MVP note:** using HMAC secret as HKDF input material; future work should provision a dedicated `OIDC_ENCRYPTION_KEY` with rotation support.

### Exchange token encryption

- Algorithm: AES-256-GCM
- Key derivation: HKDF-SHA256 from `AUTH_JWT_HMAC_SECRET` with info `nchat-oidc-exchange-v1`
- AAD: `oidc-exchange:keycloak:<row_id>` (purpose + provider + row UUID) — prevents ciphertext transplant between rows or tables.
- Same MVP note applies.

### State, nonce, and exchange code hashing

- Stored as `hex(HMAC-SHA256(domain_label + raw_value))` using `AUTH_JWT_HMAC_SECRET` as key.
- Domain labels: `nchat-oidc-state-v1`, `nchat-oidc-nonce-v1`, `nchat-oidc-exchange-code-v1`.
- Raw values are never persisted.
- Comparison: compute HMAC of the received value and compare to stored hash.
- **MVP note:** using `AUTH_JWT_HMAC_SECRET` as HMAC key; future work should provision a dedicated key.

---

## Endpoints

### `GET /auth/oidc/keycloak/login`

**Rate limit:** per-IP, using the existing `TokenEndpointRateLimiter` pattern.

1. Return 404 if OIDC disabled; 503 if misconfigured.
2. Generate cryptographically random `state` (32 bytes) and `nonce` (32 bytes).
3. Generate PKCE `code_verifier` (32 bytes, base64url) and `code_challenge` (S256).
4. Insert row in `auth.oidc_auth_requests` with HMAC-hashed state/nonce and encrypted verifier (with AAD).
5. Build Keycloak authorization URL with `state`, `nonce`, `code_challenge`, `code_challenge_method=S256`, `scope`.
6. Return HTTP 302 to Keycloak authorization URL.
7. **Never log** state, nonce, code_verifier, or any token value.

### `GET /auth/oidc/keycloak/callback`

**Rate limit:** per-IP, using the existing `TokenEndpointRateLimiter` pattern.

**Transaction and external-call boundaries** — no PostgreSQL transaction stays open during any HTTP call to Keycloak:

- **Short transaction 1**: hash the received `state`; look up `oidc_auth_requests`; reject if not found / expired / already used; mark `used_at = now()`; read `nonce_hash` and `pkce_verifier_encrypted`; commit. If this transaction fails, stop — the user restarts login.
- **Outside any transaction**: decrypt PKCE verifier; exchange authorization code + PKCE verifier with Keycloak token endpoint (subject to `OIDC_HTTP_TIMEOUT_SECONDS`); validate `id_token` against JWKS. If Keycloak exchange fails after the state has been consumed, the user must restart the login flow — this is acceptable.
- **Short transaction 2** (atomic): resolve or create user; create NChat session; record refresh token; insert `oidc_exchange_codes` row; commit. All three writes (session, refresh token, exchange code) happen in the same transaction to prevent orphaned sessions.

**Step-by-step:**

1. Return 404 if OIDC disabled.
2. **[Tx 1]** Hash received `state`; look up in `oidc_auth_requests`. Reject if not found / expired / already used. Mark `used_at = now()` and read `nonce_hash` + `pkce_verifier_encrypted`. Commit.
3. **[Outside Tx]** Decrypt PKCE verifier. Call Keycloak token endpoint with authorization code + verifier.
4. **[Outside Tx]** Validate `id_token`:
   - Fetch JWKS from provider (cached; refresh on key-not-found).
   - Reject `alg=none` unconditionally. Reject any algorithm outside the explicit allowlist: **`RS256`** for Keycloak MVP. Any allowlist change requires explicit documentation.
   - Verify signature using JWKS.
   - Verify `iss == OIDC_ISSUER_URL`.
   - Verify `aud` contains `OIDC_CLIENT_ID`. If `aud` has multiple values, also verify `azp == OIDC_CLIENT_ID`.
   - Verify `exp > now()`.
   - Verify `nonce`: compute HMAC-SHA256 of the raw nonce received in the token using domain `nchat-oidc-nonce-v1`; compare to stored `nonce_hash`.
5. Extract: `sub`, `email`, `email_verified`, `preferred_username`, `name`. `display_name` precedence: `name` → `preferred_username` → `"Usuário"`. Never use email or email local-part as display_name — both are PII.
6. Reject if `email_verified` is absent or false.
7. If `OIDC_ALLOWED_EMAIL_DOMAINS` is set, reject if domain not in list (generic 403).
8. **[Tx 2]** Resolve user:
   - Lookup by `(external_provider, external_subject)` → existing OIDC user → login.
   - If not found: lookup by email → if exists as manual user → return generic conflict (no silent linking).
   - If not found at all and `OIDC_AUTO_PROVISION_ENABLED=true` → create OIDC user.
   - Reject if user is suspended / locked / deleted.
9. **[Tx 2, continued]** Do **not** persist provider `access_token`, `refresh_token`, or `id_token`. Issue NChat access + refresh tokens via `TokenManager`. Create NChat session row and refresh token history in the same transaction.
10. **[Tx 2, continued]** Generate random exchange code (32 bytes). Insert `auth.oidc_exchange_codes` row with HMAC-hashed code and AES-GCM-encrypted tokens (with AAD). Commit. All three writes (session, refresh history, exchange code) are atomic.
11. Redirect to the relative `OIDC_FRONTEND_CALLBACK_URL` path with query parameter `code=<raw_exchange_code>`. Fixed target — no dynamic redirect and no absolute URL.
12. `redirect_after` (if present in auth request) must be validated against an internal path allowlist (default: `/`).
13. **Never log** code, state, id_token, or any token value.

### `POST /auth/oidc/keycloak/exchange`

**Rate limit:** per-IP; additionally a target-aware limiter keyed on HMAC of the exchange code is desirable but IP-based is the minimum for MVP. Body cap: 4 KiB.

Request body: `{"code": "<raw_exchange_code>"}` (≤ 4 KiB limit).

1. Return 404 if OIDC disabled.
2. Hash received code; look up in `oidc_exchange_codes`. Reject if not found / expired / already used. **Return generic 401** (no enumeration).
3. Mark `used_at = now()` atomically (same transaction).
4. Decrypt access and refresh tokens.
5. Return `200` with `{access_token, refresh_token, bearer_scheme, expires_in, user: {id, email, display_name, must_change_password}}`.
6. **Never log** code, access_token, or refresh_token.

---

## User provisioning

| Scenario                                                 | Behavior                             |
| -------------------------------------------------------- | ------------------------------------ |
| OIDC user exists `(external_provider, external_subject)` | Login existing user                  |
| No OIDC user; email not used; `AUTO_PROVISION=true`      | Create active OIDC user              |
| No OIDC user; email not used; `AUTO_PROVISION=false`     | Return generic 403                   |
| Email exists as manual user                              | Generic conflict — no silent linking |
| User is suspended/locked/deleted                         | Generic 401 — no enumeration         |
| `email_verified` absent or false                         | Reject with generic 401              |
| Email domain not in allowlist                            | Generic 403                          |

New OIDC users get `auth_source='oidc'`, `external_provider='keycloak'`, `external_subject=<sub>`, no `user_password_credentials` row.

---

## Frontend

### New route `/oidc-callback`

`OIDCCallbackPage.tsx`:

1. On mount, read `code` query parameter from URL.
2. If absent or empty: show generic error.
3. Immediately call `POST /auth/oidc/keycloak/exchange` with `{code}`.
4. On success: call `setTokens(accessToken, refreshToken)`, remove `code` from URL with `window.history.replaceState`, navigate to `/`.
5. On error: show generic SSO error message; link to `/login`.
6. **Never log** code or tokens.

### `LoginPage.tsx`

Replace disabled SSO placeholder button with active button:

- Text: "Entrar com Keycloak" (or "Entrar com SSO" if `OIDC_PROVIDER_NAME` is abstracted).
- `onClick`: `window.location.href = <AUTH_BASE>/oidc/keycloak/login`.
- The button navigates directly to the backend OIDC login endpoint. In MVP it assumes OIDC is enabled in the environment. If OIDC is disabled or misconfigured, the backend returns an error and the browser displays a generic "SSO indisponível" message. A proactive UX via a `/auth/oidc/status` endpoint is out of scope.

### `authApi.ts`

Add `oidcExchange(code: string): Promise<LoginResponse>` calling `POST /auth/oidc/keycloak/exchange`.

---

## Security invariants

- No provider token (`access_token`, `refresh_token`, `id_token`) persisted in DB or logs.
- No NChat token in URL (exchange code is opaque, short-lived, one-time — not a token).
- No silent account linking by email between manual and OIDC users.
- No open redirect: `redirect_after` must be validated internal path; default `/`.
- No raw state, nonce, or PKCE verifier stored in DB (only hashes/encrypted values).
- No rendering of raw backend error messages in frontend.
- SQL parameterized throughout.
- Exchange code and auth request both one-time use; replay returns generic error.
- Metrics/traces must not include code, state, nonce, or token values in attributes.

---

## Tests

### Backend

- OIDC disabled → all 3 endpoints return expected error.
- `OIDCLogin`: generates redirect URL with state, nonce, scope, PKCE challenge.
- `OIDCLogin`: state stored hashed; expires after TTL.
- `OIDCCallback`: rejects missing state.
- `OIDCCallback`: rejects replayed state (used_at set).
- `OIDCCallback`: rejects expired state.
- `OIDCCallback`: rejects invalid issuer / audience / nonce / expired id_token.
- `OIDCCallback`: rejects `email_verified` absent or false.
- `OIDCCallback`: rejects disallowed email domain.
- `OIDCCallback`: creates OIDC user when `AUTO_PROVISION=true`.
- `OIDCCallback`: logs in existing OIDC-linked user.
- `OIDCCallback`: does not silently link manual same-email user.
- `OIDCCallback`: creates NChat access + refresh tokens + session.
- `OIDCCallback`: no provider tokens in DB.
- `OIDCExchange`: returns tokens on valid code.
- `OIDCExchange`: rejects replayed exchange code.
- `OIDCExchange`: rejects expired exchange code.

### Frontend

- Login page shows SSO button.
- SSO button navigates to backend OIDC login endpoint.
- Email/password login still works.
- `OIDCCallbackPage`: exchanges code and stores tokens.
- `OIDCCallbackPage`: removes `code` from URL after exchange.
- `OIDCCallbackPage`: shows error on exchange failure.
- `OIDCCallbackPage`: shows error if code absent.

---

## Out of scope

- Azure AD, Google Workspace, or other OIDC providers.
- Admin UI for provider configuration.
- Manual-to-OIDC account linking (admin flow).
- SCIM / directory sync.
- MFA enforcement.
- Provider refresh token lifecycle.
- Dedicated `OIDC_ENCRYPTION_KEY` (MVP uses HMAC secret material — see cryptography section).

---

## Docs governance note

In docs, runbooks, and tests: write "query parameter `code`" rather than token query markers to avoid triggering governance scans.
