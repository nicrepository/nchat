# Runbook: Staging Auth and Session Validation

> **⚠ STAGING ONLY** — All commands target a staging environment.
> Never run against production. Never commit secrets or token values.
> Live staging tests were not run during authoring; credentials must be provided at runtime.

---

## Overview

Validates the auth-service backend contract in staging: login, token refresh, logout, session
revocation, token expiry, and SSO smoke. No production auth behavior is changed by this runbook.

Accompanies `scripts/staging/auth-smoke.mjs` (automated) and manual checklists for flows that
cannot be automated without human interaction.

---

## Prerequisites

- Node.js ≥ 20
- Network access to the staging API (`STAGING_API_BASE_URL`)
- Dedicated **test accounts** (see [Test Account Setup](#test-account-setup))
- Staging environment with HTTPS origin exactly matching `STAGING_ALLOWED_ORIGIN`

---

## Required Environment Variables

| Variable                              | Required    | Description                                                                                     |
| ------------------------------------- | ----------- | ----------------------------------------------------------------------------------------------- |
| `STAGING_API_BASE_URL`                | ✅          | Auth-service base URL — no userinfo, query, or fragment                                         |
| `STAGING_ALLOWED_ORIGIN`              | ✅          | Exact expected API origin — mandatory for all runs including local                              |
| `STAGING_AUTH_EMAIL`                  | ✅          | Primary test account e-mail — local-part must start with `nchat-smoke-` or `nchat-test-`        |
| `STAGING_AUTH_PASSWORD`               | ✅          | Primary test account password                                                                   |
| `STAGING_TEST_ACCOUNT_CONFIRM`        | ✅          | Must equal `STAGING_AUTH_EMAIL` exactly — required, not an alternative to naming policy         |
| `STAGING_AUTH_DESTRUCTIVE_CONFIRM`    | ✅          | Must equal `I_UNDERSTAND_THIS_REVOKES_TEST_SESSIONS`                                            |
| `STAGING_WEB_BASE_URL`                | Manual only | Web app base URL for browser checks                                                             |
| `STAGING_ALLOW_LOCAL`                 | Optional    | `true` to allow `http://localhost` / `http://127.0.0.1` (STAGING_ALLOWED_ORIGIN still required) |
| `STAGING_TEST_ACCOUNT_DOMAIN`         | Optional    | Allowed test account domain (alternative to `nchat-smoke-`/`nchat-test-` prefix)                |
| `STAGING_SECOND_AUTH_EMAIL`           | Optional    | Second test account e-mail (must be set together with password)                                 |
| `STAGING_SECOND_AUTH_PASSWORD`        | Optional    | Second test account password (must be set together with email)                                  |
| `STAGING_SECOND_TEST_ACCOUNT_CONFIRM` | When used   | Must equal `STAGING_SECOND_AUTH_EMAIL` exactly                                                  |
| `STAGING_OIDC_ISSUER_URL`             | Optional    | OIDC issuer base URL (HTTPS, no userinfo); enables suite E                                      |
| `STAGING_EXPECT_SHORT_TTL`            | Optional    | `true` to run expiry assertions (requires short TTL staging config)                             |
| `STAGING_REQUEST_TIMEOUT_MS`          | Optional    | Per-request timeout in ms, positive integer 1000–30000 (default: 15000)                         |

> ⛔ Never put these variables in `.env` files committed to the repository.
> Use your CI secret store or a local shell profile.

---

## Test Account Setup

1. Create dedicated accounts by invitation: `POST /admin/invites` with the admin
   bootstrap token while the workspace is still uninitialized, or
   `POST /auth/admin/invites` with a workspace-admin session afterwards. Accept
   the invite at `POST /auth/invites/accept` to complete the account.
   (The bootstrap token is never used in browser or frontend paths.)
2. Email local-part must start with `nchat-smoke-` or `nchat-test-` (e.g. `nchat-smoke-auth@example.test`).
   Alternatively, set `STAGING_TEST_ACCOUNT_DOMAIN` to an allowed test domain.
3. Both the naming/domain policy AND exact confirmation (`STAGING_TEST_ACCOUNT_CONFIRM`) are required.
   Confirmation is not an alternative to naming policy — both must be satisfied.
4. Store credentials in your team's secrets store — never in this file.
5. Accounts must have **active** status and `must_change_password: false`.
6. For cross-user revocation checks (suite C3), provision a second account with the same policy.
7. Accounts provisioned ad hoc must be cleaned up after testing (see [Cleanup](#cleanup)).

---

## Running the Automated Smoke Test

```bash
export STAGING_API_BASE_URL="https://api.staging.example.com"
export STAGING_ALLOWED_ORIGIN="https://api.staging.example.com"
export STAGING_AUTH_EMAIL="nchat-smoke-test@example.test"
export STAGING_AUTH_PASSWORD="<secret>"
export STAGING_TEST_ACCOUNT_CONFIRM="nchat-smoke-test@example.test"
export STAGING_AUTH_DESTRUCTIVE_CONFIRM="I_UNDERSTAND_THIS_REVOKES_TEST_SESSIONS"
node scripts/staging/auth-smoke.mjs
```

With cross-user revocation check:

```bash
export STAGING_SECOND_AUTH_EMAIL="nchat-smoke-test-2@example.test"
export STAGING_SECOND_AUTH_PASSWORD="<secret>"
export STAGING_SECOND_TEST_ACCOUNT_CONFIRM="nchat-smoke-test-2@example.test"
node scripts/staging/auth-smoke.mjs
```

With OIDC validation (requires `STAGING_OIDC_ISSUER_URL`):

```bash
export STAGING_OIDC_ISSUER_URL="https://keycloak.staging.example.com/realms/nchat"
node scripts/staging/auth-smoke.mjs
```

With expiry assertions (requires short TTL staging config — see [Suite D](#d-token-expiry)):

```bash
STAGING_EXPECT_SHORT_TTL=true node scripts/staging/auth-smoke.mjs
```

---

## Script Syntax and Format Check

```bash
node --check scripts/staging/auth-smoke.mjs
pnpm exec prettier scripts/staging/auth-smoke.mjs --check
```

---

## Validation Scenarios

### A. Basic Auth Smoke

| Check                                                                 | Expected result                                                                        |
| --------------------------------------------------------------------- | -------------------------------------------------------------------------------------- |
| Login with valid credentials                                          | `200` + `access_token`, `refresh_token`, `token_type: "Bearer"`, `expires_in`, `user`  |
| Access token calls `GET /auth/me/sessions` (active-session protected) | `200` + `{ data: [...] }`                                                              |
| No `Authorization` header on protected endpoint                       | `401`                                                                                  |
| Wrong password                                                        | `401` + `{ error: { code: "invalid_credentials" } }` — generic, no account enumeration |
| No token values in stdout                                             | Verified: script uses structural checks only, no token values printed                  |

### B. Refresh Flow

| Check                                                  | Expected result                                                       |
| ------------------------------------------------------ | --------------------------------------------------------------------- |
| `POST /auth/refresh` with valid token                  | `200` + new token pair; new refresh token differs from old (rotation) |
| Replay old (rotated) refresh token                     | `401` (reuse detected, session family revoked)                        |
| Current rotated refresh token after family revocation  | `401`                                                                 |
| Access token from rotated pair after family revocation | `401` on active-session endpoint                                      |
| Arbitrary invalid refresh token                        | `401`                                                                 |

> The frontend `authenticatedFetch` behavior (late-arrival guard, concurrency guard,
> session-binding guard) is covered by unit tests in `apps/web/src/lib/authClient.test.ts`.
> This suite validates the **backend contract only**.

### C. Logout and Session Revocation

| Check                                                           | Expected result                                 |
| --------------------------------------------------------------- | ----------------------------------------------- |
| `GET /auth/me/sessions` after login                             | ≥ 1 session; `current: true` on current session |
| `POST /auth/logout`                                             | `204`                                           |
| Refresh token after logout                                      | `401`                                           |
| Access token after logout (active-session endpoint)             | `401` (session revoked in DB)                   |
| Before/after session diff after second login                    | Exactly 1 new session ID detected               |
| `DELETE /auth/me/sessions/{id}` (own non-current session)       | `204`                                           |
| Revoked session refresh token                                   | `401`                                           |
| Revoked session access token (active-session endpoint)          | `401`                                           |
| Current session remains valid after revoking other session      | `200`                                           |
| Cross-user `DELETE /auth/me/sessions/{id}` (suite C3, optional) | `404` or `401`                                  |

> Note: `POST /auth/logout` is idempotent and not a token-validity oracle — invalid,
> revoked, or unknown refresh tokens return `204`. Access token invalidation after logout
> is confirmed via `GET /auth/me/sessions`, which uses `RequireActiveSession` middleware.

### D. Token Expiry

> **Do not wait for default TTL (15 min access / 30 day refresh) in CI.**

**Operational path (automated with `STAGING_EXPECT_SHORT_TTL=true`):**

1. Deploy staging with `AUTH_ACCESS_TOKEN_TTL_SECONDS=5` (or similarly short value).
2. Run the smoke test with `STAGING_EXPECT_SHORT_TTL=true`.
3. The script waits 12 s, then asserts `401` on the expired access token and that refresh still works.
4. Restore staging TTL config to production defaults after the test window.

```bash
STAGING_EXPECT_SHORT_TTL=true node scripts/staging/auth-smoke.mjs
```

**Manual checklist (when short TTL deployment is not available):**

These are optional sign-off checks, not the primary automated path:

- [ ] Login and note `idle_expires_at` from `GET /auth/me/sessions`
- [ ] After `idle_expires_at`: access token → `401`; refresh token → `401`
- [ ] After `absolute_expires_at`: refresh token → `401` even if not idle

> ⛔ Never add a background keepalive timer. The backend is authoritative for expiry.

### E. SSO / OIDC Smoke

Suite E is **skipped by default**. Enable by setting `STAGING_OIDC_ISSUER_URL`.

| Check                                                                      | Expected result            |
| -------------------------------------------------------------------------- | -------------------------- |
| `GET /auth/oidc/keycloak/login`                                            | `302`/`307`/`308` redirect |
| Redirect uses HTTPS                                                        | ✅                         |
| Redirect origin matches configured issuer exactly                          | ✅                         |
| Redirect has no userinfo                                                   | ✅                         |
| Redirect includes `state`, `client_id`/`response_type` params              | ✅                         |
| Decoded query and fragment keys contain no credential-bearing token fields | ✅                         |

**Full OIDC callback flow (Manual — requires dedicated test identity in Keycloak):**

1. Open `STAGING_WEB_BASE_URL/login` in a browser.
2. Click "Entrar com Keycloak".
3. Authenticate with a dedicated test identity (never use personal credentials).
4. Verify redirect to `/oidc-callback?code=...`.
5. Verify the `code` parameter disappears before navigating to `/`.
6. Verify home page loads and session appears in `GET /auth/me/sessions`.

### F. Security Surface Checks

| Check                                                                           | Expected result    |
| ------------------------------------------------------------------------------- | ------------------ |
| Login response has no `X-NChat-Admin-Token` header                              | ✅                 |
| Error response `Content-Type`                                                   | `application/json` |
| OIDC redirect: no credential-bearing token fields in query or fragment (parsed) | ✅                 |

---

## Cleanup

The script logs out all sessions it creates using `finally` blocks and a `cleanupLogout` helper.
Cleanup failures are collected and **cause a non-zero exit** — the script reports them so leaked
sessions can be identified and revoked manually.

If the script is **interrupted before session IDs are collected**, stale sessions may remain.
Clean up manually:

```bash
# Login to get a fresh access token, then revoke all other sessions
curl -s -X DELETE "${STAGING_API_BASE_URL}/auth/me/sessions" \
  -H "Authorization: Bearer <fresh_access_token>"
# Then logout the current session
curl -s -X POST "${STAGING_API_BASE_URL}/auth/logout" \
  -H "Content-Type: application/json" \
  -d '{"refresh_token":"<current_refresh_token>"}'
```

> Note: `DELETE /auth/me/sessions` (bulk) preserves the **current** session; it only revokes
> all other sessions. The current session must be explicitly logged out afterward.

If specific session IDs are known, revoke them directly:

```bash
curl -s -X DELETE "${STAGING_API_BASE_URL}/auth/me/sessions/<session_id>" \
  -H "Authorization: Bearer <access_token>"
```

If sessions cannot be cleaned up via API (e.g., access token expired and cannot be refreshed),
use the staging admin panel to manage or deactivate the test account's sessions.

---

## Manual Expiry Checks (Optional Sign-off)

Run only when short TTL staging deployment is not available:

- [ ] Login → note `idle_expires_at` and `absolute_expires_at`
- [ ] After `idle_expires_at`: `GET /auth/me/sessions` with original AT → `401`; `POST /auth/refresh` → `401`
- [ ] After `absolute_expires_at`: `POST /auth/refresh` → `401` even if not idle

---

## Known Limitations

- Expiry suite (D) requires staging deployment with a short access token TTL, or manual checks.
- Full OIDC automation requires a dedicated Keycloak test identity not set up by this runbook.
- Cross-user revocation check (C3) requires a second test account.
- Frontend `authenticatedFetch` concurrency scenarios (late-arrival guard, session-binding guard)
  are covered by unit tests only; staging cannot simulate concurrent requests without a load
  test harness, which is out of scope.
- The live staging suite was not run during authoring; credentials must be provided at runtime.

---

## Out of Scope

The following are explicitly out of scope for this runbook and script:

- Auth feature changes or new auth functionality
- RBAC implementation
- Production load testing or performance benchmarking
- Real user credentials (dedicated test accounts only)
- Changing default token TTL values in production config
- Using the admin bootstrap token in browser or frontend runtime paths
- Destructive tests against non-test-account sessions

---

## Related Runbooks and Tests

- `docs/runbooks/task-jwt-access-refresh.md` — JWT and refresh token architecture
- `docs/runbooks/task-device-session-management.md` — session and device management endpoints
- `apps/web/src/lib/authClient.test.ts` — frontend refresh concurrency unit tests
- `apps/web/e2e/auth.spec.ts` — Playwright e2e (mocked backend, browser flows)
