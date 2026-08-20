# Admin Service — Admin API

> **Scope:** the Admin Console foundation (issue #578). This document covers the
> endpoints that exist today: the administrative session handshake, the console
> bootstrap, and the audit trail. Administrative management of users, channels,
> integrations and infrastructure is **not** implemented and is not described
> here.

Public base: `/api/admin`. The gateways rewrite `/api/admin/<rest>` to
`/<rest>` before the request reaches the pod, so the paths admin-service
registers carry no public prefix.

| Layer                             | Rule                                                                |
| --------------------------------- | ------------------------------------------------------------------- |
| Local Traefik                     | `strip-admin-prefix` in `infra/traefik/local/dynamic.yml`           |
| k3s-dev / k3s-staging / nchat-dev | `Middleware admin-api-prefix`, referenced by the Ingress annotation |

`scripts/ci/admin-route-contract-check.sh` renders every overlay and fails if a
middleware is missing, an overlay diverges, the backend adds an `/api/` alias,
or the console's base path drifts. Kubernetes probes reach `/healthz` and
`/readyz` on the pod directly and are unaffected.

Every response uses the platform envelope: `{"data": …}` on success,
`{"error": {"code": …, "message": …}}` on failure.

---

## Contents

| #   | Method | Path                      | Auth                              |
| --- | ------ | ------------------------- | --------------------------------- |
| 1   | POST   | `/api/admin/session`      | NChat access token (Bearer)       |
| 2   | DELETE | `/api/admin/session`      | Admin session cookie + CSRF token |
| 3   | GET    | `/api/admin/bootstrap`    | Admin session cookie              |
| 4   | GET    | `/api/admin/audit/events` | Admin session cookie + capability |

---

## The credential

The console's credential is an opaque, server-generated value in a cookie:

```
__Host-nchat_admin_session=<opaque>; Path=/; HttpOnly; Secure; SameSite=Strict; Max-Age=900
```

- `HttpOnly` — unreadable from JavaScript, so an XSS foothold on the console
  cannot exfiltrate it.
- `SameSite=Strict` — withheld on every cross-site request, which is the primary
  CSRF defence.
- `__Host-` prefix — the browser refuses the cookie unless it is `Secure`,
  `Path=/` and carries no `Domain`. A sibling subdomain (the chat host, for
  instance) therefore cannot set or read it.

`X-NChat-Admin-Token` (`ADMIN_BOOTSTRAP_TOKEN`) plays no part in any endpoint
here. It remains a CLI-only bootstrap credential on the three `/admin/*` routes
of auth-service and must never be sent to a browser.

## CSRF

Mutating requests must echo the token returned in the bootstrap payload:

```
X-NChat-Admin-CSRF: <csrf_token>
```

The token is derived server-side from the session identifier, so it is bound to
exactly one session and cannot be transplanted. The request `Origin` (or
`Referer`, when the browser omits `Origin`) must also be the deployment's own or
one of `ADMIN_ALLOWED_ORIGINS`; a request carrying neither header is refused.

---

## 1. `POST /api/admin/session`

Exchanges a proven NChat identity for an administrative session. This is the
only endpoint that accepts a chat access token, and it accepts it for one
purpose: proving who is asking. It authorizes nothing on its own.

**Request**

```
Authorization: Bearer <nchat access token>
```

No body. Every other input — the actor, the originating login session, the
client address, the user agent — is derived server-side.

**Responses**

| Status | Meaning                                                                              |
| ------ | ------------------------------------------------------------------------------------ |
| `201`  | Session created. `Set-Cookie` carries the credential; body is the bootstrap payload. |
| `401`  | Missing, malformed, expired or revoked access token.                                 |
| `403`  | Authenticated, but not an active platform administrator, or holding no capability.   |
| `429`  | Rate limited (per client address).                                                   |
| `503`  | Admin API not configured on this pod.                                                |

## 2. `DELETE /api/admin/session`

Revokes the administrative session and expires the cookie. Idempotent: it never
reveals whether the presented credential matched anything.

| Status | Meaning                                |
| ------ | -------------------------------------- |
| `204`  | Session ended, cookie cleared.         |
| `401`  | No live administrative session.        |
| `403`  | Origin rejected or CSRF token invalid. |

## 3. `GET /api/admin/bootstrap`

What the console shell loads before rendering anything.

```json
{
  "data": {
    "identity": {
      "user_id": "…",
      "email": "admin@example.test",
      "display_name": "Admin",
      "avatar_url": "/api/auth/avatars/…"
    },
    "capabilities": ["admin.audit.read"],
    "environment": "PRODUCTION",
    "build": { "service": "admin-service", "version": "1.2.3", "commit": "abc1234" },
    "session": {
      "idle_expires_at": "2026-08-20T12:00:00Z",
      "absolute_expires_at": "2026-08-20T20:00:00Z"
    },
    "csrf_token": "…"
  }
}
```

The payload is an allowlist, not a filter: the response struct names every field
that may leave the service. It carries **no** access token, refresh token,
bootstrap token, service token, DSN, password, client secret, Kubernetes
credential, environment dump or infrastructure topology, and the shape is
asserted field-by-field by `TestBootstrap_LeaksNoCredentialOrInfrastructureDetail`.

`environment` comes from the deployment's own `APP_ENV` and is one of
`DEVELOPMENT`, `STAGING`, `PRODUCTION`. An unrecognised value resolves to
`PRODUCTION`. No request header, hostname, query parameter or stored preference
participates.

`capabilities` is presentation data. It tells the console which navigation
entries to render; it never tells an endpoint what to allow.

| Status | Meaning                                              |
| ------ | ---------------------------------------------------- |
| `200`  | Session live.                                        |
| `401`  | No session, or the session or its login was revoked. |
| `403`  | The principal is no longer an active administrator.  |
| `503`  | Admin API not configured on this pod.                |

## 4. `GET /api/admin/audit/events`

Requires the `admin.audit.read` capability.

**Query**

| Name    | Default | Notes                                          |
| ------- | ------- | ---------------------------------------------- |
| `limit` | `50`    | Positive integer, capped server-side at `200`. |

```json
{
  "data": {
    "events": [
      {
        "id": "7",
        "occurred_at": "2026-08-20T10:00:00Z",
        "actor_user_id": "…",
        "actor_email": "admin@example.test",
        "action": "admin.session.create",
        "resource": "admin.session",
        "result": "success",
        "correlation_id": "…"
      }
    ]
  }
}
```

The endpoint takes no resource identifier: it returns the platform-wide trail or
refuses, so there is no object reference for a caller to tamper with.

| Status | Meaning                               |
| ------ | ------------------------------------- |
| `200`  | Events returned, newest first.        |
| `400`  | `limit` is not a positive integer.    |
| `401`  | No live administrative session.       |
| `403`  | Missing `admin.audit.read`.           |
| `503`  | Admin API not configured on this pod. |

---

## Single sign-on

The console's sign-in link is `/api/auth/oidc/keycloak/login?app=admin`. `app`
is a closed enum (`chat` | `admin`) that selects which provider callback URI
auth-service uses; it is a label, never a destination, and an unknown value is
refused with `400 invalid_oidc_app`. The chosen context is stored server-side
with the OIDC state and recovered at the callback, so nothing the returning
request carries decides where the browser ends up.

`OIDC_ADMIN_REDIRECT_URL` must name this host's
`/api/auth/oidc/keycloak/callback`, and that URI must be registered on the same
Keycloak client as the chat one. Leaving it unset means the console has no
single sign-on — it never falls back to the chat host's URI.

A successful SSO login yields an ordinary NChat session. It grants no
administrative authority: `POST /api/admin/session` still refuses anyone who is
not an active principal.

## Capabilities

Declared in `services/admin-service/internal/domain/capability.go` and
constrained by a `CHECK` on `auth.admin_role_capabilities` (migration 000008),
so a capability the platform does not define cannot be granted:

`admin.superuser`, `admin.users.read`, `admin.users.manage`,
`admin.channels.read`, `admin.channels.manage`, `admin.security.read`,
`admin.security.manage`, `admin.integrations.read`,
`admin.integrations.manage`, `admin.infrastructure.read`,
`admin.infrastructure.manage`, `admin.audit.read`, `admin.config.read`,
`admin.config.manage`.

`admin.superuser` implies the rest. An unknown capability is refused even for a
superuser. See `docs/security/rbac-matrix.md` for the policy and
`docs/runbooks/task-admin-console-foundation.md` for how a principal is granted.
