# Auth Service — API Endpoints

> **Scope:** MVP implementation. This document covers implemented endpoints only.
> RBAC, full admin browser flows, Azure AD/Google Workspace SSO, and `RF-75` are
> **not** implemented. Keycloak is the current and only SSO entry point.

Base URL resolved at runtime via `VITE_AUTH_API_BASE_URL` (default: `/api/auth`).
Admin endpoints use `VITE_ADMIN_API_BASE_URL` (default: `/api/admin`).

---

## Table of Contents

| #   | Method | Path                             | Auth                                   | RF           |
| --- | ------ | -------------------------------- | -------------------------------------- | ------------ |
| 1   | POST   | `/auth/login`                    | Public                                 | —            |
| 2   | POST   | `/auth/logout`                   | Public (refresh token)                 | —            |
| 3   | POST   | `/auth/refresh`                  | Public (refresh token)                 | RF-51        |
| 4   | POST   | `/auth/password/forgot`          | Public                                 | RF-48        |
| 5   | POST   | `/auth/password/reset`           | Public (reset token)                   | RF-48        |
| 6   | POST   | `/auth/invites/accept`           | Public (invite token)                  | RF-46        |
| 7   | GET    | `/auth/oidc/keycloak/login`      | Public                                 | RF-44        |
| 8   | GET    | `/auth/oidc/keycloak/callback`   | Public (OIDC state)                    | RF-44        |
| 9   | POST   | `/auth/oidc/keycloak/exchange`   | Public (one-time code)                 | RF-44        |
| 10  | GET    | `/auth/me/login-attempts`        | Bearer JWT + active session            | —            |
| 11  | GET    | `/auth/me/sessions`              | Bearer JWT + active session            | RF-51, RF-53 |
| 12  | DELETE | `/auth/me/sessions`              | Bearer JWT + active session            | RF-53        |
| 13  | DELETE | `/auth/me/sessions/{session_id}` | Bearer JWT + active session            | RF-53        |
| 14  | GET    | `/auth/me/devices`               | Bearer JWT + active session            | RF-52, RF-53 |
| 15  | DELETE | `/auth/me/devices/{device_id}`   | Bearer JWT + active session            | RF-53        |
| 16  | PATCH  | `/auth/me/devices/{device_id}`   | Bearer JWT + active session            | RF-53        |
| 18  | POST   | `/admin/invites`                 | Bootstrap-only, initialization window  | RF-46        |
| 21  | POST   | `/auth/admin/invites`            | Bearer JWT + session + workspace admin | RF-46        |

---

## Workspace-scoped invitations (issue #433)

`POST /auth/admin/invites` is the browser-callable invite endpoint. It is
guarded by `BearerAuth` → `RequireActiveSession` → `RequireWorkspaceAdmin`, in
that order.

`RequireWorkspaceAdmin` derives both the actor and the workspace server-side:
the actor is the JWT subject, and the workspace is resolved from that actor's
own **active** `owner` or `admin` membership in an **active** workspace
(`chat.workspace_members`).

**Workspace resolution.** The service never picks a tenant on the caller's
behalf:

| Administered workspaces | `X-NChat-Workspace-Id`      | Result                             |
| ----------------------- | --------------------------- | ---------------------------------- |
| none                    | any                         | `403`                              |
| exactly one             | absent                      | that workspace                     |
| two or more             | absent                      | `409 workspace_selection_required` |
| any                     | one the caller administers  | that workspace                     |
| any                     | anything else, or malformed | `403`                              |

`X-NChat-Workspace-Id` is a **selector, not authority**. It is checked against
the caller's own memberships with the same predicate used to authorize them, so
it can only narrow the answer, never widen it: naming a workspace where the
caller is a plain member, one that is inactive, or one that does not exist are
all the same `403`, and none of them reveals which case applied. A caller who
administers a single workspace gets that workspace whatever they send.

Multiplicity is a refusal rather than a choice on purpose — silently acting on
one of several tenants is exactly the failure this replaces. The `409` body
names no workspace and does not disclose how many the caller administers.

| Condition                                        | Status |
| ------------------------------------------------ | ------ |
| Missing/invalid token                            | `401`  |
| Revoked or expired session                       | `401`  |
| Authenticated, administers no workspace          | `403`  |
| Selector not among the caller's admin workspaces | `403`  |
| Several administered, none selected              | `409`  |
| Service booted without a database                | `503`  |

### `POST /auth/admin/invites`

Creates an invite **bound to the caller's workspace**. Body: `email`,
`display_name`, optional `full_name`. Any other field — including `role` or
`workspace_id` — is ignored at decode time, so the endpoint cannot be used to
grant privileges or to target another tenant.

Responses: `201` (bare object `{id, email, created_at}`, matching the existing
bootstrap invite contract), `400` invalid payload or e-mail, `409` conflict (or
`workspace_selection_required`, see above), `429` rate limited (with
`Retry-After`), `503` e-mail handoff disabled.

The invite `409` is deliberately one code for three causes — the address already
belongs to a member of this workspace, an invite is already pending for it here,
or the partial unique index rejected a race. Distinguishing them would report
whether an address is present in a workspace the caller may not administer. The
selection `409` carries its own error code, so a client can tell "resend with a
workspace selected" from "this will never succeed".

#### Workspace binding and invite kind

`auth.user_invites.workspace_id` (migration `auth/000008`, foreign key added in
`chat/000019`) records the issuing workspace, and `invited_by_user_id` records
the issuing admin. Both come from the session.

`auth.user_invites.invite_kind` records what the invite confers, and is set by
the issuing code path rather than by any request field:

| Kind              | Issued by                  | Membership on acceptance |
| ----------------- | -------------------------- | ------------------------ |
| `member`          | `POST /auth/admin/invites` | `member`                 |
| `bootstrap_owner` | `POST /admin/invites`      | `owner`                  |

A `kind`, `role`, `owner` or `admin` field in the body reaches nothing: the
authenticated route always writes `member` and the bootstrap route always writes
`bootstrap_owner`, overwriting whatever they were handed.

Accepting an invite runs as one transaction: it locks the invite row
`FOR UPDATE`, resolves or creates the global identity for the address, writes a
`chat.workspace_members` row for **the invite's** workspace with the role its
kind confers, joins the workspace's `#geral` channel, and marks the invite
accepted. A failure at any step rolls the whole thing back, so there is no state
where an invite is consumed without a membership, a membership exists while the
token is still reusable, or a workspace is left half-initialized.

**Legacy invites.** Rows written before `auth/000008` carry no workspace.
The migration leaves them exactly as it found them — same status, same
timestamps — which is what makes it reversible, and the acceptance path refuses
them instead, reporting the same `401 invalid_invite_token` as any other
unusable token.

Identity stays global — one address is one account, with memberships in as many
workspaces as invited it. Accepting an invite for an address that already has an
account adds the membership and **does not** touch the existing password: the
submitted password is only used when the acceptance creates the account.

Consequences worth stating:

- Two workspaces can invite the same address independently; neither blocks the
  other, and the pending-invite uniqueness is per workspace.
- An admin of workspace A can never produce a membership in workspace B.
- Re-accepting an already-consumed token is rejected; a concurrent double
  accept produces exactly one membership.

#### Rate limiting

| Control               | Scope             | Default       | Env var                                                                     |
| --------------------- | ----------------- | ------------- | --------------------------------------------------------------------------- |
| Authoritative budget  | actor × workspace | 10 per 10 min | `AUTH_INVITE_RATE_LIMIT_PER_ACTOR`, `AUTH_INVITE_RATE_LIMIT_WINDOW_MINUTES` |
| Complementary ceiling | client IP         | 30 per hour   | `AUTH_INVITE_RATE_LIMIT_PER_IP_PER_HOUR`                                    |

Both are counted in PostgreSQL, so both hold across replicas and survive a
restart. The authoritative budget is counted inside the creating transaction,
under an advisory lock keyed by `(workspace, actor)`, so it cannot be raced. The
IP ceiling is a separate shared counter, keyed by
`admin-invites-ip:<client-ip>` in a fixed hourly window and charged before the
handler runs; it is the complementary control, not the authoritative one,
because an address is a much weaker identity than an authenticated actor.

Values outside their permitted range fall back to the default rather than being
clamped, so a typo cannot silently weaken the control.

On rejection: `429`, `Retry-After` set to the window, and **nothing persisted** —
no invite row, no outbox entry, therefore no e-mail. The invitee's address is
never a rate-limit key, so nobody can be locked out by having someone else's
invites throttled. The limiter runs after authentication and authorization, so
an unauthorized caller is refused with `403` without consuming any budget.

---

## Error response envelope

All error responses follow a common JSON envelope:

```json
{
  "error": "<error_code>",
  "message": "<human-readable message>"
}
```

Common error codes:

| Code                    | HTTP | Description                                 |
| ----------------------- | ---- | ------------------------------------------- |
| `bad_request`           | 400  | Malformed JSON or invalid field             |
| `invalid_credentials`   | 401  | Wrong email/password or account locked      |
| `invalid_refresh_token` | 401  | Expired or revoked refresh token            |
| `invalid_token`         | 401  | Expired or used password-reset token        |
| `invalid_invite_token`  | 401  | Expired or used invite token                |
| `unauthorized`          | 401  | Missing or invalid Bearer token             |
| `forbidden`             | 403  | Operation not permitted                     |
| `oidc_disabled`         | 404  | OIDC feature flag off                       |
| `conflict`              | 409  | Email already registered or invite conflict |
| `request_too_large`     | 413  | Body exceeds 4 KiB                          |
| `invalid_transition`    | 422  | Status transition not allowed               |
| `service_unavailable`   | 503  | Dependency not configured (DB, email, JWT)  |
| `internal_error`        | 500  | Unexpected server error                     |

---

## Endpoints

---

### 1. POST /auth/login

**Purpose:** Authenticate a user with email and password. Returns NChat JWT access and refresh token pair.

**Auth requirement:** Public

**Rate limit:** Token endpoint limiter (per IP, per minute); 401 response for lockout matches invalid credentials to prevent enumeration.

**Request body:**

```json
{
  "email": "<user@example.com>",
  "password": "<plaintext_password>",
  "device_fingerprint": "<opaque_device_fingerprint_or_omit>",
  "device_name": "NIC Chat Web",
  "platform": "web"
}
```

Fields `device_fingerprint`, `device_name`, and `platform` are optional. The frontend
currently sends `device_name: "NIC Chat Web"` and omits `device_fingerprint`.

**Response — 200 OK:**

```json
{
  "access_token": "<jwt_access_token>",
  "refresh_token": "<opaque_refresh_token>",
  "token_type": "Bearer",
  "expires_in": 900,
  "user": {
    "id": "<uuid>",
    "email": "<user@example.com>",
    "display_name": "<Display Name>",
    "must_change_password": false
  }
}
```

`expires_in` is in seconds (configured via `AUTH_ACCESS_TOKEN_TTL_SECONDS`).

**Common errors:**

| Code                  | HTTP | Condition                                                     |
| --------------------- | ---- | ------------------------------------------------------------- |
| `invalid_credentials` | 401  | Wrong email, wrong password, locked account, or inactive user |
| `bad_request`         | 400  | Malformed JSON                                                |
| `service_unavailable` | 503  | Database not configured                                       |

**Security notes:**

- All credential errors (wrong password, lockout, unknown email) return the same
  `invalid_credentials` 401 to prevent user enumeration.
- Access token is a signed HMAC-SHA256 JWT. Never log or expose it in URLs.
- Refresh token is an opaque random value; only its SHA-256 hash is stored.
- IP address is collected for audit (RF-49) using trusted-proxy-aware extraction.

**Frontend screen:** `LoginPage.tsx` — `/login`

---

### 2. POST /auth/logout

**Purpose:** Invalidate the current refresh token, revoking the associated session.

**Auth requirement:** Public (refresh token in body)

**Rate limit:** Token endpoint limiter (per IP)

**Request body:**

```json
{
  "refresh_token": "<opaque_refresh_token>"
}
```

**Response — 204 No Content** (success or already-invalid token — idempotent)

**Common errors:**

| Code                  | HTTP | Condition                           |
| --------------------- | ---- | ----------------------------------- |
| `bad_request`         | 400  | Malformed JSON                      |
| `service_unavailable` | 503  | Auth session manager not configured |

**Security notes:**

- An already-invalid or unknown refresh token also returns 204. This prevents
  attackers from testing whether a token is still valid.
- Never include the refresh token in URL query parameters.

**Frontend usage:** `authApi.ts → logout()`

---

### 3. POST /auth/refresh

**Purpose:** Exchange a valid refresh token for a new access + refresh token pair (sliding session). RF-51.

**Auth requirement:** Public (refresh token in body)

**Rate limit:** Token endpoint limiter (per IP)

**Request body:**

```json
{
  "refresh_token": "<opaque_refresh_token>"
}
```

**Response — 200 OK:**

```json
{
  "access_token": "<new_jwt_access_token>",
  "refresh_token": "<new_opaque_refresh_token>",
  "token_type": "Bearer",
  "expires_in": 900
}
```

**Common errors:**

| Code                    | HTTP | Condition                                  |
| ----------------------- | ---- | ------------------------------------------ |
| `invalid_refresh_token` | 401  | Expired, revoked, or unknown refresh token |
| `bad_request`           | 400  | Malformed JSON                             |
| `service_unavailable`   | 503  | Auth session manager not configured        |

**Security notes:**

- The old refresh token is invalidated on exchange (token rotation).
- Frontend performs silent refresh reactively: `authenticatedFetch` in `authClient.ts`
  retries once with a new token pair when an authenticated endpoint returns 401.
  There is no background timer and no proactive refresh before expiry.
  Backend remains authoritative for idle and absolute session expiry.

**Frontend usage:** `authApi.ts → refresh()` called by `authClient.ts → authenticatedFetch`

**RF mapping:** RF-51 (session expiry / refresh)

---

### 4. POST /auth/password/forgot

**Purpose:** Trigger a password-reset email for the given address. RF-48.

**Auth requirement:** Public

**Rate limit:** IP limiter + per-email target limiter (both must pass)

> **Dependency:** Requires email handoff service to be configured. Returns 503 otherwise.

**Request body:**

```json
{
  "email": "<user@example.com>"
}
```

**Response — 202 Accepted** (always, whether the address exists or not)

**Common errors:**

| Code                  | HTTP | Condition                    |
| --------------------- | ---- | ---------------------------- |
| `service_unavailable` | 503  | Email handoff not configured |
| `bad_request`         | 400  | Malformed JSON               |

**Security notes:**

- Always returns 202 regardless of whether the email matches a registered account.
  This prevents account enumeration.
- The raw reset token is never stored — only its SHA-256 hash. Raw token is
  delivered only via email.

**Frontend screen:** `ForgotPasswordPage.tsx` — `/forgot-password`

**RF mapping:** RF-48 (password recovery)

---

### 5. POST /auth/password/reset

**Purpose:** Set a new password using a valid single-use reset token. RF-48.

**Auth requirement:** Public (reset token in body)

**Rate limit:** IP limiter + per-token target limiter

**Request body:**

```json
{
  "token": "<opaque_reset_token_from_email>",
  "new_password": "<new_plaintext_password>"
}
```

**Response — 204 No Content** (success)

**Common errors:**

| Code                  | HTTP | Condition                                   |
| --------------------- | ---- | ------------------------------------------- |
| `invalid_token`       | 401  | Expired, used, or unknown reset token       |
| `bad_request`         | 400  | Malformed JSON or password policy violation |
| `service_unavailable` | 503  | Password recovery manager not configured    |

**Security notes:**

- Token is single-use: `used_at` is set atomically on consumption.
- Password must pass the policy configured in `auth_policy_settings`
  (default: ≥12 chars, uppercase, lowercase, number, symbol).
- Token arrives in the browser URL from the email link (`/reset-password?<reset-token-param>=<opaque-reset-token>`).
  `ResetPasswordPage.tsx` removes the token query parameter on mount via `replace` navigation,
  so the token does not persist in browser URL or history.
  The API receives the token only in the POST request body, not as a URL query parameter.

**Frontend screen:** `ResetPasswordPage.tsx` — `/reset-password?<reset-token-param>=<opaque-reset-token>`

**RF mapping:** RF-48 (password recovery)

---

### 6. POST /auth/invites/accept

**Purpose:** Activate an invited account by consuming a single-use invite token and setting the initial password. RF-46.

**Auth requirement:** Public (invite token in body)

**Rate limit:** IP limiter + per-token target limiter

**Request body:**

```json
{
  "token": "<opaque_invite_token_from_email>",
  "display_name": "<How I Want to Be Known>",
  "full_name": "<Full Legal Name>",
  "password": "<initial_password>"
}
```

`full_name` is optional.

**Response — 201 Created:**

```json
{
  "id": "<uuid>",
  "email": "<user@example.com>",
  "display_name": "<How I Want to Be Known>",
  "full_name": "<Full Legal Name>",
  "created_at": "2025-01-01T00:00:00Z"
}
```

`full_name` is omitted from the response when not provided.

**Common errors:**

| Code                   | HTTP | Condition                                   |
| ---------------------- | ---- | ------------------------------------------- |
| `invalid_invite_token` | 401  | Expired, used, or unknown invite token      |
| `conflict`             | 409  | Email already registered                    |
| `bad_request`          | 400  | Malformed JSON or password policy violation |
| `service_unavailable`  | 503  | Invite manager not configured               |

**Security notes:**

- Invite token is single-use (consumed atomically).
- Token arrives in the browser URL from the email link (`/accept-invite?<invite-token-param>=<opaque-invite-token>`).
  `AcceptInvitePage.tsx` removes the token query parameter on mount via `replace` navigation,
  so the token does not persist in browser URL or history.
  The API receives the token only in the POST request body, not as a URL query parameter.
- Password policy enforced on first activation.
- Accepting an invite creates the user account and activates it in one step.

**Frontend screen:** `AcceptInvitePage.tsx` — `/accept-invite?<invite-token-param>=<opaque-invite-token>`

**RF mapping:** RF-46 (invite-based registration)

---

### 7. GET /auth/oidc/keycloak/login

**Purpose:** Initiate SSO login via Keycloak Authorization Code + PKCE flow. RF-44.

**Auth requirement:** Public

**Rate limit:** OIDC IP limiter (when OIDC enabled)

> **Feature flag:** Returns 404 `oidc_disabled` when `OIDC_ENABLED=false`.

**Request:** No body. Browser navigates directly to this URL.

**Response — 302 Found:** Redirect to Keycloak authorization endpoint with `state` and `code_challenge`.

**Common errors:**

| Code               | HTTP | Condition                             |
| ------------------ | ---- | ------------------------------------- |
| `oidc_disabled`    | 404  | `OIDC_ENABLED` is false               |
| `oidc_unavailable` | 503  | Keycloak unreachable or misconfigured |

**Security notes:**

- `state` and `nonce` parameters are generated per-request and validated on callback.
- PKCE (`code_challenge_method=S256`) is used; `code_verifier` is stored server-side.
- This endpoint redirects the browser; it must not be called via `fetch`.

**Frontend usage:** `authApi.ts → oidcLoginUrl()` — used as `href` in `LoginPage.tsx`

**RF mapping:** RF-44 (SSO/OIDC)

---

### 8. GET /auth/oidc/keycloak/callback

**Purpose:** Consume the Keycloak callback, validate `state`/`nonce`/ID token, create an NChat session, then redirect the browser to `/oidc-callback?code=<one-time-code>`. RF-44.

**Auth requirement:** Public (validated via OIDC `state` parameter)

**Rate limit:** OIDC IP limiter (when OIDC enabled)

> **Feature flag:** Returns 404 `oidc_disabled` when `OIDC_ENABLED=false`.

**Query parameters (set by Keycloak):**

| Param   | Description                                |
| ------- | ------------------------------------------ |
| `code`  | Authorization code from Keycloak           |
| `state` | Server-generated state for CSRF protection |

**Response — 302 Found:** Redirect to `/oidc-callback?code=<opaque_one_time_code>` on success.

**Common errors:**

| Code                     | HTTP | Condition                                        |
| ------------------------ | ---- | ------------------------------------------------ |
| `oidc_disabled`          | 404  | `OIDC_ENABLED` is false                          |
| `oidc_unavailable`       | 503  | Keycloak unreachable or misconfigured            |
| `invalid_oidc_callback`  | 401  | Invalid state, nonce, or ID token                |
| `oidc_login_unavailable` | 403  | Domain not in allowlist or provisioning disabled |
| `account_link_required`  | 409  | OIDC subject matches different local account     |

**Security notes:**

- NChat access/refresh tokens are **never** placed in URL query parameters.
  Only an opaque one-time code (short-lived) appears in the redirect URL.
- The one-time code is consumed by a subsequent `POST /auth/oidc/keycloak/exchange`.
- The redirect target is validated against a strict allowlist
  (`/oidc-callback` path only; no external redirect).
- IP address is collected for audit using trusted-proxy-aware extraction.

**Frontend screen:** Browser follows redirect to `/oidc-callback` (handled by `OIDCCallbackPage.tsx`)

**RF mapping:** RF-44 (SSO/OIDC)

---

### 9. POST /auth/oidc/keycloak/exchange

**Purpose:** Exchange the opaque one-time OIDC code (from the callback redirect) for NChat JWT tokens. RF-44.

**Auth requirement:** Public (one-time code in body; short-lived, single-use)

**Rate limit:** OIDC IP limiter (when OIDC enabled)

> **Feature flag:** Returns 404 `oidc_disabled` when `OIDC_ENABLED=false`.

**Request body:**

```json
{
  "code": "<opaque_one_time_code_from_callback_url>"
}
```

**Response — 200 OK:**

```json
{
  "access_token": "<jwt_access_token>",
  "refresh_token": "<opaque_refresh_token>",
  "token_type": "Bearer",
  "expires_in": 900,
  "user": {
    "id": "<uuid>",
    "email": "<user@example.com>",
    "display_name": "<Display Name>",
    "must_change_password": false
  }
}
```

Same shape as `/auth/login` response.

**Common errors:**

| Code                    | HTTP | Condition                             |
| ----------------------- | ---- | ------------------------------------- |
| `oidc_disabled`         | 404  | `OIDC_ENABLED` is false               |
| `invalid_oidc_callback` | 401  | Expired or unknown one-time code      |
| `service_unavailable`   | 503  | OIDC or JWT dependency not configured |

**Security notes:**

- The frontend strips the `?code=` from the browser URL via `history.replaceState`
  before making this POST to prevent the code from leaking in the `Referer` header.
- The `OIDCCallbackPage.tsx` uses a single-call guard (`useRef`) to prevent
  duplicate exchange under React StrictMode double-invoke.

**Frontend screen:** `OIDCCallbackPage.tsx` — `/oidc-callback`

**RF mapping:** RF-44 (SSO/OIDC)

---

### 10. GET /auth/me/login-attempts

**Purpose:** Return the authenticated user's failed login attempt history (paginated, cursor-based).

**Auth requirement:** Bearer JWT + active session

**Query parameters:**

| Param    | Default | Max | Description                          |
| -------- | ------- | --- | ------------------------------------ |
| `limit`  | 50      | 100 | Items per page                       |
| `cursor` | —       | —   | Opaque cursor from previous response |

**Response — 200 OK:**

```json
{
  "data": [
    {
      "id": "<bigint_as_string>",
      "email": "<user@example.com>",
      "ip_address": "192.168.1.*.*",
      "user_agent": "Mozilla/5.0 ...",
      "failure_reason": "invalid_credentials",
      "created_at": "2025-01-01T00:00:00Z"
    }
  ],
  "pagination": {
    "limit": 50,
    "next_cursor": "<opaque_cursor_or_null>"
  }
}
```

**Privacy:** `ip_address` is masked (IPv4: `a.b.*.*`; IPv6: `prefix:*`).
`user_agent` is truncated to 200 printable characters.

**Common errors:**

| Code                  | HTTP | Condition                                 |
| --------------------- | ---- | ----------------------------------------- |
| `unauthorized`        | 401  | Missing, invalid, or expired Bearer token |
| `bad_request`         | 400  | Invalid cursor                            |
| `service_unavailable` | 503  | Endpoint disabled (missing JWT config)    |

---

### 11. GET /auth/me/sessions

**Purpose:** List the authenticated user's sessions (active by default), newest first. RF-51, RF-53.

**Auth requirement:** Bearer JWT + active session

**Query parameters:**

| Param             | Default      | Description                          |
| ----------------- | ------------ | ------------------------------------ |
| `limit`           | 50 (max 100) | Items per page                       |
| `include_revoked` | false        | Include revoked sessions when `true` |

**Response — 200 OK:**

```json
{
  "data": [
    {
      "id": "<uuid>",
      "device_id": "<uuid_or_null>",
      "created_at": "2025-01-01T00:00:00Z",
      "last_seen_at": "2025-01-01T00:00:00Z",
      "idle_expires_at": "2025-01-01T01:00:00Z",
      "absolute_expires_at": "2025-08-01T00:00:00Z",
      "revoked_at": null,
      "ip_address": "192.168.1.*.*",
      "user_agent": "Mozilla/5.0 ...",
      "current": true
    }
  ],
  "pagination": {
    "limit": 50,
    "next_cursor": null
  }
}
```

`current: true` marks the session associated with the current access token.
`absolute_expires_at` may be null if absolute expiry is not configured.

**Privacy:** `ip_address` masked; `user_agent` sanitized. Raw `refresh_token_hash`
and `device_fingerprint_hash` are never returned.

**Common errors:** Same as endpoint 10.

**RF mapping:** RF-51 (session expiry), RF-53 (device/session management)

---

### 12. DELETE /auth/me/sessions

**Purpose:** Revoke all sessions for the authenticated user **except** the current session. RF-53.

**Auth requirement:** Bearer JWT + active session

**Request:** No body.

**Response — 204 No Content**

**Common errors:**

| Code           | HTTP | Condition                        |
| -------------- | ---- | -------------------------------- |
| `unauthorized` | 401  | No current session ID in context |

**RF mapping:** RF-53

---

### 13. DELETE /auth/me/sessions/{session_id}

**Purpose:** Revoke a specific session owned by the authenticated user. RF-53.

**Auth requirement:** Bearer JWT + active session

**Path parameter:** `session_id` — UUID of session to revoke.

**Response — 204 No Content**

**Common errors:**

| Code          | HTTP | Condition                        |
| ------------- | ---- | -------------------------------- |
| `bad_request` | 400  | `session_id` is not a valid UUID |
| `not_found`   | 404  | Unknown or cross-user session    |

**RF mapping:** RF-53

---

### 14. GET /auth/me/devices

**Purpose:** List the authenticated user's linked devices. RF-52, RF-53.

**Auth requirement:** Bearer JWT + active session

**Query parameters:**

| Param             | Default      | Description                         |
| ----------------- | ------------ | ----------------------------------- |
| `limit`           | 50 (max 100) | Items per page                      |
| `include_revoked` | false        | Include revoked devices when `true` |

**Response — 200 OK:**

```json
{
  "data": [
    {
      "id": "<uuid>",
      "display_name": "<My Laptop>",
      "platform": "web",
      "last_ip": "192.168.1.*.*",
      "first_seen_at": "2025-01-01T00:00:00Z",
      "last_seen_at": "2025-01-01T00:00:00Z",
      "revoked_at": null,
      "session_count": 1,
      "current": true
    }
  ],
  "meta": {
    "max_devices_per_user": 5
  },
  "pagination": {
    "limit": 50,
    "next_cursor": null
  }
}
```

`current: true` marks the device linked to the current session.
`meta.max_devices_per_user` is the configured policy limit.
`display_name` and `platform` may be null.

**Privacy:** `last_ip` masked. Raw `device_fingerprint_hash` never returned.

**Common errors:** Same as endpoint 10.

**RF mapping:** RF-52 (device limit), RF-53 (device management)

---

### 15. DELETE /auth/me/devices/{device_id}

**Purpose:** Revoke a device and all its sessions. RF-53.

**Auth requirement:** Bearer JWT + active session

**Path parameter:** `device_id` — UUID of device to revoke.

**Response — 204 No Content** (idempotent for own already-revoked device)

**Common errors:**

| Code          | HTTP | Condition                       |
| ------------- | ---- | ------------------------------- |
| `bad_request` | 400  | `device_id` is not a valid UUID |
| `not_found`   | 404  | Unknown or cross-user device    |

**RF mapping:** RF-53

---

### 16. PATCH /auth/me/devices/{device_id}

**Purpose:** Update the display name of an active device.

**Auth requirement:** Bearer JWT + active session

**Path parameter:** `device_id` — UUID of device to rename.

**Request body:**

```json
{
  "display_name": "<New Device Name>"
}
```

**Response — 204 No Content**

**Common errors:**

| Code          | HTTP | Condition                              |
| ------------- | ---- | -------------------------------------- |
| `bad_request` | 400  | Malformed UUID or invalid display_name |
| `not_found`   | 404  | Unknown, revoked, or cross-user device |

---

### 18. POST /admin/invites

**Purpose:** Create an invite for a new user. Triggers invite email delivery. RF-46.

**Auth requirement:** Bootstrap-only — `X-NChat-Admin-Token: <token>` header

> ⚠️ **Initialization only (issue #433).** This route exists to break a
> chicken-and-egg: no HTTP route creates a workspace membership except invite
> acceptance, so a fresh deployment has no owner/admin and every
> session-scoped admin route answers `403`. Once the workspace has an active
> owner or admin, use `POST /auth/admin/invites` instead — this one starts
> refusing.
>
> It is **disabled unless both** `ADMIN_BOOTSTRAP_TOKEN` and
> `AUTH_BOOTSTRAP_WORKSPACE_ID` are set. Requires email handoff (503 otherwise).

**This is the only route the bootstrap credential reaches.** It used to have two
siblings — `POST /admin/users` and `PATCH /admin/users/{id}/status` — which
created and suspended **global** identities and, unlike this route, never
consulted the bootstrap lifecycle: a leaked credential kept reach over every
account in the deployment indefinitely. They are **removed**, not guarded, and
now answer `404` for everyone in every lifecycle state. Bootstrapping has
exactly one door, and it closes.

**Canonical bootstrap sequence:**

1. Generate a strong credential and set `ADMIN_BOOTSTRAP_TOKEN` together with
   `AUTH_BOOTSTRAP_WORKSPACE_ID` (see the credential policy above; the service
   refuses to start on a weak or half-configured pair).
2. `POST /admin/invites` with the invitee's `email` and `display_name`. The
   invite is issued as `bootstrap_owner`, into the configured workspace, by a
   system identity — none of which is expressible in the request.
3. The invitee accepts it at `POST /auth/invites/accept`.
4. Acceptance creates the workspace's **first `owner`** membership.
5. That closes the window: this route now answers `503`, and any other pending
   `bootstrap_owner` invite for the workspace is revoked in the same
   transaction.
6. Everything afterwards goes through the authenticated, workspace-scoped API —
   `POST /auth/admin/invites` — which derives its actor and workspace from the
   session. Unset `ADMIN_BOOTSTRAP_TOKEN` once step 4 is done.

**Credential policy.** `ADMIN_BOOTSTRAP_TOKEN` must be exactly 32 random bytes
encoded as unpadded Base64URL — 43 characters, one canonical format. Generate
one with:

```sh
openssl rand -base64 32 | tr '+/' '-_' | tr -d '='
```

The configuration has exactly three valid states, checked at **startup**, not
per request:

| `ADMIN_BOOTSTRAP_TOKEN` | `AUTH_BOOTSTRAP_WORKSPACE_ID` | Outcome                                 |
| ----------------------- | ----------------------------- | --------------------------------------- |
| unset                   | unset                         | starts; endpoint disabled (503)         |
| valid                   | valid UUID                    | starts; endpoint live until first owner |
| anything else           | anything else                 | **service refuses to start**            |

Refused at startup: a token that is not 43 Base64URL characters, does not
decode to 32 bytes, carries leading or trailing whitespace, is a repeated
character (`AAAA…` is valid Base64URL for 32 zero bytes), or contains a known
placeholder marker such as `changeme`, `secret`, `admin`, `bootstrap`, `token`
or `example`. A token without a workspace, or a workspace without a token, is
also refused — both halves fail closed at runtime, so a half-configured
deployment would look enabled and answer 503 forever.

The credential never appears in a log line, an error message, a trace or a
metric label, and no hash of it is recorded either.

**Guessing budget.** Every route behind the bootstrap credential is rate
limited **before** the credential is compared, so an attacker does not get
unlimited online guesses at a secret that can mint an owner invite.

| Setting                                    | Default | Range  |
| ------------------------------------------ | ------- | ------ |
| `AUTH_BOOTSTRAP_RATE_LIMIT_ATTEMPTS`       | `5`     | 1–100  |
| `AUTH_BOOTSTRAP_RATE_LIMIT_WINDOW_MINUTES` | `15`    | 1–1440 |

The counter lives in PostgreSQL (`auth.bootstrap_auth_attempts`, migration
`auth/000009`), keyed by `bootstrap-admin-token:<client-ip>` — never by the
credential, which would give each guess its own budget. Being in the database
rather than in process memory is the point: an attacker must not get one budget
per replica, nor a fresh one on restart. `/admin/invites` is the only route the
credential reaches, so it is the only one spending this budget.

Consequences:

- the budget is charged on **every** attempt, valid or not, and a correct
  credential neither resets nor refunds it — a stolen credential being replayed
  is exactly when the limit should still apply;
- a limited request returns `429` with `Retry-After` and never reaches the
  credential comparison, so it reads no invite, writes no outbox row and cannot
  affect the bootstrap lifecycle;
- the response is identical whether the credential was right, wrong or absent;
- an `X-NChat-Admin-Token` header over 256 bytes is rejected generically and
  still spends budget, so padding it is not a free probe;
- if the counter is **unreachable** the request is refused with `503` and the
  credential is not compared. It fails closed: an unbounded credential check is
  worse than a briefly unavailable bootstrap endpoint, and falling back to a
  per-process count would silently restore the multi-replica hole.

Zero is never "unlimited" — a value outside the ranges above falls back to the
default. Disable the endpoint by unsetting the credential.

**Server-side authority.** Neither the workspace nor the issuer is expressible
in the request:

| Value        | Source                                                                     |
| ------------ | -------------------------------------------------------------------------- |
| Workspace    | `AUTH_BOOTSTRAP_WORKSPACE_ID` — configuration only, never a lookup or body |
| Issuer       | System identity; stored as `invited_by_user_id = NULL`                     |
| Invite kind  | Fixed `bootstrap_owner`                                                    |
| Invitee role | Fixed `owner` on acceptance — this is the workspace's first administrator  |

A `workspace_id`, `actor_id`, `kind` or `role` in the body is discarded at
decode time, and `X-NChat-Workspace-Id` is not read on this route at all.

**Lifecycle.** Refused with `503` when: not configured, or the target workspace
already has an active owner/admin. Both report the same message and neither
reveals which workspace or who administers it. A failure to determine that
state is also a refusal — it fails closed rather than reopening the window.

**Closing the window.** Accepting a `bootstrap_owner` invite creates an `owner`
membership, which is precisely the condition this route checks — so the first
acceptance closes the window, and every subsequent call answers `503`. The
acceptance takes a transaction-scoped advisory lock on the workspace before
locking the invite row and re-checks for an existing administrator while holding
it, so two acceptances racing in one workspace produce exactly one owner; the
loser is refused and writes nothing. In the same transaction, any other pending
`bootstrap_owner` invite for that workspace is revoked, so no outstanding
bootstrap token survives initialization.

An ordinary invite is unaffected: it still creates a `member` and never closes
the bootstrap window.

**The window stays shut.** "Has this workspace ever gained an administrator?" is
a question about its history and is answered independently of whether the
workspace is currently operational. Archiving a workspace does **not**
un-initialize it, so the bootstrap credential cannot be used to mint a new
`bootstrap_owner` invite for a workspace that already had an owner and was later
archived — nor could accepting such an invite grant ownership that materialises
on reactivation.

Both issuance and acceptance require all three of: the workspace exists, it is
`active`, and it has no active `owner`/`admin` membership. Anything else is a
refusal — `503` on issuance, `401 invalid_invite_token` on acceptance — and the
refusals are indistinguishable from one another.

| Workspace exists | Status     | Has owner/admin | Bootstrap |
| ---------------- | ---------- | --------------- | --------- |
| yes              | `active`   | no              | **open**  |
| yes              | `active`   | yes             | shut      |
| yes              | `disabled` | yes             | shut      |
| yes              | `disabled` | no              | shut      |
| no               | —          | —               | shut      |

A failure to determine that state is also a refusal: it fails closed rather than
reopening the window on a transient outage.

**Rate limit.** Same budget as the authenticated route, counted in PostgreSQL.
Bootstrap invites share one budget per workspace, because they share one
(NULL) issuer.

**Operational disablement.** Unset `AUTH_BOOTSTRAP_WORKSPACE_ID` (or
`ADMIN_BOOTSTRAP_TOKEN`) once the first administrator exists. The lifecycle
check already refuses at that point, so unsetting is defence in depth.

**Request body:**

```json
{
  "email": "<invitee@example.com>",
  "display_name": "<Suggested Display Name>",
  "full_name": "<Suggested Full Name>"
}
```

`display_name` and `full_name` are optional prefill suggestions; the user sets
their own values on invite acceptance.

**Response — 201 Created:**

```json
{
  "id": "<invite_uuid>",
  "email": "<invitee@example.com>",
  "created_at": "2025-01-01T00:00:00Z"
}
```

**Common errors:**

| Code                  | HTTP | Condition                                          |
| --------------------- | ---- | -------------------------------------------------- |
| `unauthorized`        | 401  | Missing or wrong `X-NChat-Admin-Token`             |
| `conflict`            | 409  | Email already registered or invite already pending |
| `bad_request`         | 400  | Malformed JSON or invalid email                    |
| `service_unavailable` | 503  | Email handoff not configured or DB not configured  |

**RF mapping:** RF-46 (invite-based registration)

---

---

## Implementation boundaries

| Boundary                        | Status                                         |
| ------------------------------- | ---------------------------------------------- |
| Email/password login            | ✅ Implemented                                 |
| JWT access + refresh tokens     | ✅ Implemented                                 |
| Session idle/absolute expiry    | ✅ Implemented (RF-51)                         |
| Invite-based registration       | ✅ Implemented (RF-46)                         |
| Password recovery via email     | ✅ Implemented (RF-48)                         |
| Keycloak OIDC SSO               | ✅ Implemented (RF-44)                         |
| Device and session list/revoke  | ✅ Implemented (RF-52, RF-53)                  |
| Failed login audit              | ✅ Implemented (RF-49)                         |
| Admin user status (bootstrap)   | ✅ Foundation only — not browser-callable      |
| Admin user status (browser/JWT) | ❌ Not implemented (RF-74 deferred)            |
| Full RBAC                       | ❌ Not implemented                             |
| Azure AD / Google Workspace SSO | ❌ Not implemented                             |
| `must_change_password` flow     | ❌ Not implemented (UI shows advisory message) |
| RF-75 admin browser flow        | ❌ Not implemented                             |
