# Runbook: JWT Access and Refresh Tokens

## Overview

Adds the auth-service foundation for JWT access tokens and opaque refresh tokens.
The implementation exposes:

- `POST /auth/refresh`
- `POST /auth/logout`

Access tokens are HS256 JWTs signed with `AUTH_JWT_HMAC_SECRET`. Refresh tokens
are opaque random values; only an HMAC-SHA-256 hash is stored in
`auth.user_sessions.refresh_token_hash`.

## Out of scope

This task does not implement:

- Email/password login
- OAuth/OIDC
- Frontend auth flows
- RBAC or final authorization middleware

## Endpoints

### POST /auth/refresh

Request:

```json
{
  "refresh_token": "opaque-refresh-token"
}
```

Response `200`:

```json
{
  "access_token": "jwt-access-token",
  "refresh_token": "new-opaque-refresh-token",
  "token_type": "Bearer",
  "expires_in": 900
}
```

Behavior:

- Requires configured JWT secret and database-backed auth session store.
- Hashes the provided refresh token before lookup.
- Verifies the session row exists in `auth.user_sessions`, is not revoked, is not
  idle/absolute expired, and belongs to an active, non-deleted `auth.users` row.
- Rotates the refresh token by replacing `refresh_token_hash` atomically in the
  same transaction and updating `last_seen_at` plus `idle_expires_at`.
- Returns a new JWT access token with `sub`, `sid`, `iss`, `aud`, `iat`, `nbf`,
  `exp`, and `jti` claims.

Error responses:

- `400` invalid JSON or missing `refresh_token`
- `401` invalid, expired, or revoked refresh session
- `500` internal error
- `503` token endpoints disabled because JWT config or DB setup is unavailable

### POST /auth/logout

Request:

```json
{
  "refresh_token": "opaque-refresh-token"
}
```

Response:

```text
204 No Content
```

Behavior:

- Hashes the provided refresh token before persistence.
- Revokes the matching `auth.user_sessions` row in a transaction by setting
  `revoked_at = now()` and `revoked_reason = 'logout'`.

## Environment variables

| Variable                         | Required | Default      | Notes                                                                    |
| -------------------------------- | -------- | ------------ | ------------------------------------------------------------------------ |
| `AUTH_JWT_HMAC_SECRET`           | Yes      | none         | HS256 secret; must be at least 32 bytes. Empty/short disables endpoints. |
| `AUTH_JWT_ISSUER`                | No       | `nchat-auth` | Expected `iss` claim.                                                    |
| `AUTH_JWT_AUDIENCE`              | No       | `nchat-api`  | Expected `aud` claim.                                                    |
| `AUTH_ACCESS_TOKEN_TTL_SECONDS`  | No       | `900`        | Access token TTL, default 15 minutes.                                    |
| `AUTH_REFRESH_TOKEN_TTL_SECONDS` | No       | `2592000`    | Refresh token idle TTL, default 30 days.                                 |
| `DATABASE_URL`                   | Yes      | none         | Required for session lookup, rotation, and logout.                       |

## Security notes

- Raw refresh tokens are never stored; the service stores only HMAC-SHA-256 hashes.
- Access and refresh tokens are not logged by handlers, metrics, or tracing.
- The token response does not include token hashes.
- SQL uses explicit `auth.` schema references and parameterized queries only.
- Refresh rotation and logout use database transactions.
- The old refresh token is invalid after refresh because its stored hash is replaced
  atomically with the new token hash.

## Validation

Unit coverage includes:

- JWT generation and validation
- Expired access token rejection
- Wrong-secret rejection
- Missing/short JWT secret disabling endpoint wiring
- Refresh token hash not equal to the raw token
- Refresh token rotation
- Revoked/expired refresh rejection
- Logout revocation
- Response shape excluding token hashes

Local commands:

```bash
cd services/auth-service && go test ./...
pnpm format:check
pnpm run ci
make ci
```

Manual DB exercise was intentionally not added in this task because login/session
issuance is out of scope and no test-session helper exists yet. A future login or
session-helper task should seed `auth.user_sessions.refresh_token_hash` with the same
HMAC-SHA-256 algorithm used by `TokenManager.HashRefreshToken` before curl-testing
`/auth/refresh` and `/auth/logout` against a running auth-service.

## Related

- Tracking issue: [#54](https://github.com/nicrepository/nchat/issues/54)
- Auth schema: [docs/architecture/auth-data-model.md](../architecture/auth-data-model.md)
- Admin user creation runbook: [docs/runbooks/task-23-admin-manual-user-create.md](task-23-admin-manual-user-create.md)
