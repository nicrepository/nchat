# Auth Service — API Endpoints

> **Scope:** MVP implementation. This document covers implemented endpoints only.
> RBAC, full admin browser flows, Azure AD/Google Workspace SSO, and `RF-75` are
> **not** implemented. Keycloak is the current and only SSO entry point.

Base URL resolved at runtime via `VITE_AUTH_API_BASE_URL` (default: `/api/auth`).
Browser-callable admin endpoints use `VITE_ADMIN_API_BASE_URL`
(default: `/api/auth/admin`).

> **Gateway note (issue #425).** `/api/admin` is routed to `admin-service`,
> which serves no user endpoints — a client aiming there gets `404`. Every
> browser-callable route on this service sits under `/auth/*`, and the gateways
> rewrite `/api/auth/<rest>` to `/auth/<rest>` before the request reaches the
> pod:
>
> | Layer                             | Rule                                                               |
> | --------------------------------- | ------------------------------------------------------------------ |
> | Local Traefik                     | `rewrite-auth-api-prefix` in `infra/traefik/local/dynamic.yml`     |
> | k3s-dev / k3s-staging / nchat-dev | `Middleware auth-api-prefix`, referenced by the Ingress annotation |
>
> All four use the same `replacePathRegex` (`^/api/auth/(.*)` → `/auth/${1}`).
> `scripts/ci/auth-route-contract-check.sh` renders every overlay and fails if
> a middleware is missing, an overlay diverges, the backend adds an `/api/`
> alias, or the frontend base drifts. Health probes are unaffected: Kubernetes
> probes the pod directly on `/healthz`.

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
| 17  | POST   | `/admin/users`                   | Bootstrap-only (`X-NChat-Admin-Token`) | —            |
| 18  | POST   | `/admin/invites`                 | Bootstrap-only (`X-NChat-Admin-Token`) | RF-46        |
| 19  | PATCH  | `/admin/users/{id}/status`       | Bootstrap-only (`X-NChat-Admin-Token`) | —            |
| 20  | GET    | `/auth/admin/users`              | Bearer JWT + session + workspace admin | RF-74        |

---

## Workspace user administration (issue #425)

`GET /auth/admin/users` is what the **Configurações → Usuários** screen calls.
It is guarded by `BearerAuth` → `RequireActiveSession` → `RequireWorkspaceAdmin`,
in that order.

`RequireWorkspaceAdmin` derives the workspace server-side: it is the one where
the JWT subject holds an **active** `owner` or `admin` membership in an
**active** workspace (`chat.workspace_members`). No workspace identifier is
accepted from the path, query, body or headers — the route carries none.

| Condition                               | Status |
| --------------------------------------- | ------ |
| Missing/invalid token                   | `401`  |
| Revoked or expired session              | `401`  |
| Authenticated, administers no workspace | `403`  |
| Service booted without a database       | `503`  |

### `GET /auth/admin/users`

Lists one page of the caller's workspace. Members who left are excluded;
suspended ones are included so they can be reactivated. Soft-deleted accounts
never appear.

**Query parameters**

| Name     | Default | Rules                                                                      |
| -------- | ------- | -------------------------------------------------------------------------- |
| `limit`  | `50`    | 1–100. Above 100 is clamped to 100; `0`, negative or non-numeric is `400`. |
| `cursor` | —       | Opaque token from a previous `next_cursor`. Invalid ⇒ `400`.               |

The limit is bounded because an unpaginated listing lets any administrator
force the service to materialise and serialise every member of a workspace
(CWE-400). At most `limit + 1` rows are ever read — the extra row is what
decides `has_more` without a second query.

**Response — 200**

```json
{
  "data": {
    "data": [
      {
        "id": "…",
        "email": "alice@example.com",
        "display_name": "Alice",
        "full_name": "Alice Andrade",
        "status": "active",
        "auth_source": "manual",
        "created_at": "2026-01-15T10:00:00Z"
      }
    ],
    "pagination": { "limit": 50, "next_cursor": "…", "has_more": true }
  }
}
```

The outer `data` is the service-wide envelope; the inner block mirrors the
shape `GET /auth/me/login-attempts` already publishes, so the API has one
pagination contract rather than two. `has_more` is redundant with
`next_cursor` and is sent anyway so a client need not encode the rule that an
empty cursor means the end; the two always agree.

`full_name` is omitted when unset. No password hash, token, session, avatar,
external subject, sort key or workspace identifier is returned.

**Ordering and cursor.** Rows are ordered by
`lower(coalesce(nullif(display_name, ''), email))` then by `id`. The `id`
tiebreak makes the order total — without it two identical display names have
an undefined relative position and a keyset cursor silently skips or repeats
rows. Resumption uses a row-value comparison rather than `OFFSET`, so page N
costs the same as page 1 and concurrent inserts cannot shift the window.

The cursor is base64url of a versioned JSON object carrying the workspace, the
sort key and the user id. It is opaque, not a capability: it names a position
in a list the caller is already authorized to read. A cursor with an unknown
version, an unknown field, or another workspace is rejected with a generic
`400` — the message never says which, so a cursor cannot be used to probe for
another tenant. A tampered cursor cannot widen the query either: the workspace
filter comes from the session on every request, independently of the cursor.

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

### 17. POST /admin/users

**Purpose:** Create a user account with an initial password (bootstrap/admin tooling use only).

**Auth requirement:** Bootstrap-only — `X-NChat-Admin-Token: <token>` header

> ⚠️ **Not browser-callable.** The `X-NChat-Admin-Token` must never be used in
> browser or frontend runtime code. Use CLI / server-to-server / CI tooling only.
> Returns 503 when `ADMIN_BOOTSTRAP_TOKEN` env var is not set.

**Request body:**

```json
{
  "email": "<user@example.com>",
  "display_name": "<Display Name>",
  "full_name": "<Full Legal Name>",
  "initial_password": "<initial_password>",
  "must_change_password": true
}
```

`full_name` is optional. `must_change_password` defaults to `false`
(set `true` to force password change on first login).

**Response — 201 Created:**

```json
{
  "id": "<uuid>",
  "email": "<user@example.com>",
  "display_name": "<Display Name>",
  "full_name": "<Full Legal Name>",
  "status": "active",
  "auth_source": "manual",
  "email_verified_at": "2025-01-01T00:00:00Z",
  "created_at": "2025-01-01T00:00:00Z"
}
```

**Common errors:**

| Code                  | HTTP | Condition                                            |
| --------------------- | ---- | ---------------------------------------------------- |
| `unauthorized`        | 401  | Missing or wrong `X-NChat-Admin-Token`               |
| `conflict`            | 409  | Email already registered                             |
| `bad_request`         | 400  | Malformed JSON or password policy violation          |
| `service_unavailable` | 503  | `ADMIN_BOOTSTRAP_TOKEN` not set or DB not configured |

---

### 18. POST /admin/invites

**Purpose:** Create an invite for a new user. Triggers invite email delivery. RF-46.

**Auth requirement:** Bootstrap-only — `X-NChat-Admin-Token: <token>` header

> ⚠️ Same bootstrap-only restriction as `/admin/users`.
> Requires email handoff service to be configured (503 otherwise).

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

### 19. PATCH /admin/users/{id}/status

**Purpose:** Activate (`active`) or suspend (`suspended`) a user account. Foundation/bootstrap only.

**Auth requirement:** Bootstrap-only — `X-NChat-Admin-Token: <token>` header

> ⚠️ **Not browser-callable.** The status mutation buttons in `AdminUsersPage.tsx`
> are intentionally `disabled` until a browser-safe JWT/RBAC admin guard (RF-74)
> replaces this bootstrap guard. Self-deactivation prevention is also deferred to
> that future guard.

**Path parameter:** `id` — UUID of the user to update.

**Request body:**

```json
{
  "status": "suspended"
}
```

Permitted values: `"active"` or `"suspended"`.

**Response — 200 OK:**

```json
{
  "id": "<uuid>",
  "email": "<user@example.com>",
  "display_name": "<Display Name>",
  "status": "suspended",
  "auth_source": "manual",
  "email_verified_at": "2025-01-01T00:00:00Z",
  "created_at": "2025-01-01T00:00:00Z"
}
```

**Common errors:**

| Code                  | HTTP | Condition                                            |
| --------------------- | ---- | ---------------------------------------------------- |
| `unauthorized`        | 401  | Missing or wrong `X-NChat-Admin-Token`               |
| `not_found`           | 404  | User not found                                       |
| `invalid_transition`  | 422  | Transition not allowed by domain rules               |
| `invalid_status`      | 422  | Value is not `active` or `suspended`                 |
| `service_unavailable` | 503  | `ADMIN_BOOTSTRAP_TOKEN` not set or DB not configured |

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
