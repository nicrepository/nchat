# Runbook: JWT Access and Refresh Tokens

## Overview

Adds the auth-service foundation for JWT access tokens and opaque refresh tokens.
The implementation exposes:

- `POST /auth/refresh`
- `POST /auth/logout`

Access tokens are HS256 JWTs signed with `AUTH_JWT_HMAC_SECRET`. Refresh tokens
are opaque random values; only HMAC-SHA-256 hashes are stored in
`auth.user_sessions.refresh_token_hash` and `auth.refresh_token_history.refresh_token_hash`.

## Out of scope

This task does not implement:

- Email/password login
- OAuth/OIDC
- Frontend auth flows
- RBAC or final authorization middleware
- Distributed Valkey or gateway-level token endpoint rate limiting

## Endpoints

### POST /auth/refresh

Request body limit: 4 KiB.

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
  idle/absolute expired, belongs to an active, non-deleted `auth.users` row, and
  has an active matching row in `auth.refresh_token_history`.
- Rotates the refresh token in one transaction by marking the old history row
  `rotated`, replacing `auth.user_sessions.refresh_token_hash`, inserting the new
  history row as `active`, and updating `last_seen_at` plus `idle_expires_at`.
- Detects reuse of a previously rotated refresh token. Reuse marks that history
  row `reused`, revokes the session with `revoked_reason = 'refresh_token_reuse_detected'`,
  revokes any active token history for that session family, and returns the same
  generic invalid refresh token response used for unknown tokens.
- Returns a new JWT access token with `sub`, `sid`, `iss`, `aud`, `iat`, `nbf`,
  `exp`, and `jti` claims.

Error responses:

- `400` invalid JSON or missing `refresh_token`
- `401` invalid, expired, revoked, unknown, or reused refresh token
- `413` request body exceeds 4 KiB
- `429` token endpoint rate limit exceeded
- `500` internal error
- `503` token endpoints disabled because JWT config or DB setup is unavailable

### POST /auth/logout

Request body limit: 4 KiB.

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

- Hashes the provided refresh token before lookup.
- If the current active token is provided, revokes the matching `auth.user_sessions`
  row in a transaction by setting `revoked_at = now()` and `revoked_reason = 'logout'`,
  then marks active token history for the session as `revoked`.
- Logout is idempotent and is not a token-validity oracle: invalid, revoked, reused,
  or unknown refresh tokens return `204 No Content`.

## Environment variables

| Variable                                    | Required | Default      | Notes                                                                    |
| ------------------------------------------- | -------- | ------------ | ------------------------------------------------------------------------ |
| `AUTH_JWT_HMAC_SECRET`                      | Yes      | none         | HS256 secret; must be at least 32 bytes. Empty/short disables endpoints. |
| `AUTH_JWT_ISSUER`                           | No       | `nchat-auth` | Expected `iss` claim.                                                    |
| `AUTH_JWT_AUDIENCE`                         | No       | `nchat-api`  | Expected `aud` claim.                                                    |
| `AUTH_ACCESS_TOKEN_TTL_SECONDS`             | No       | `900`        | Access token TTL, default 15 minutes.                                    |
| `AUTH_REFRESH_TOKEN_TTL_SECONDS`            | No       | `2592000`    | Refresh token idle TTL, default 30 days.                                 |
| `AUTH_TOKEN_ENDPOINT_RATE_LIMIT_PER_MINUTE` | No       | `60`         | Per-instance in-memory token endpoint refill rate per client IP.         |
| `AUTH_TOKEN_ENDPOINT_RATE_LIMIT_BURST`      | No       | `10`         | Per-instance in-memory token endpoint burst per client IP.               |
| `AUTH_TRUSTED_PROXY_CIDRS`                  | No       | empty        | Comma-separated trusted proxy CIDRs; empty = use `RemoteAddr` only.      |
| `DATABASE_URL`                              | Yes      | none         | Required for session lookup, rotation, reuse detection, and logout.      |

## Security notes

- Raw refresh tokens are never stored; the service stores only HMAC-SHA-256 hashes.
- Access and refresh tokens are not logged by handlers, metrics, or tracing.
- Token responses do not include token hashes.
- SQL uses explicit `auth.` schema references and parameterized queries only.
- Refresh rotation, reuse detection, and logout use database transactions.
- The old refresh token is invalid after refresh because its history row is marked
  `rotated` and the session stores only the new active token hash.
- Reusing a rotated refresh token revokes the session family and does not reveal
  reuse detection to the client.
- Token endpoint rate limiting uses `RemoteAddr` by default. When
  `AUTH_TRUSTED_PROXY_CIDRS` is configured, forwarded headers are honored only
  when the request `RemoteAddr` is inside a configured trusted CIDR. The
  leftmost `X-Forwarded-For` entry is used if it passes `net.ParseIP`
  validation; otherwise `X-Real-IP` is tried if valid; otherwise `RemoteAddr`
  is used. Values are canonicalized with `parsed.String()`; raw header strings
  are never used as limiter keys.
- The Kubernetes base relies on the built-in defaults for optional token TTL and
  token endpoint rate-limit settings. Keep real `AUTH_JWT_HMAC_SECRET` and
  `DATABASE_URL` values out of versioned YAML; provision them through a strict
  SealedSecret generated from a local unsealed manifest.

## Known limitations

- The token endpoint limiter is in-memory and per auth-service instance. It protects
  a single process but does not coordinate across replicas. A later Valkey-backed
  or gateway-enforced limiter can replace it without changing the endpoint contract.
- This PR does not create login/session issuance. Future login work must insert the
  initial active `auth.refresh_token_history` row when creating `auth.user_sessions`.

## Validation

Unit coverage includes:

- JWT generation and validation
- Expired access token rejection
- Wrong-secret rejection
- Missing/short JWT secret disabling endpoint wiring
- Refresh token hash not equal to the raw token
- Refresh token rotation and history insertion
- Reused rotated refresh token revoking the session family
- Active token rejection after session-family revocation
- Revoked/expired refresh rejection
- Logout revocation of session and token history
- Logout idempotency for invalid/revoked tokens
- Request body cap handling
- Token endpoint rate limiting
- Response shape excluding token hashes

Local commands:

```bash
cd services/auth-service && go test ./...
pnpm migrations:check
pnpm format:check
pnpm lint:go
pnpm test:coverage:go:check
pnpm run ci
make ci
```

Manual DB exercise was intentionally not added in this task because login/session
issuance is out of scope and no test-session helper exists yet. A future login or
session-helper task should seed both `auth.user_sessions.refresh_token_hash` and
an active `auth.refresh_token_history.refresh_token_hash` with the same HMAC-SHA-256
algorithm used by `TokenManager.HashRefreshToken` before curl-testing `/auth/refresh`
and `/auth/logout` against a running auth-service.

## Related

- Tracking issue: [#54](https://github.com/nicrepository/nchat/issues/54)
- Auth schema: [docs/architecture/auth-data-model.md](../architecture/auth-data-model.md)
- Admin user creation runbook: [docs/runbooks/task-23-admin-manual-user-create.md](task-23-admin-manual-user-create.md)
