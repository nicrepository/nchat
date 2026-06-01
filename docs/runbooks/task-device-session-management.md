# Runbook: Device and Session Management

**RF traceability:**

| ID    | Requirement                                       | Implementation                                                                                                          |
| ----- | ------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------- |
| RF-51 | Sessions expire after inactivity                  | `idle_expires_at` / `absolute_expires_at` stored in `auth.user_sessions`, surfaced in `GET /auth/me/sessions` response  |
| RF-52 | Multi-device limit configurable by admin          | `auth_policy_settings.max_devices_per_user`; surfaced in `GET /auth/me/devices` response as `meta.max_devices_per_user` |
| RF-53 | Linked device list visible and manageable by user | All 6 endpoints in this runbook                                                                                         |
| RF-54 | New-device notifications                          | **Out of scope** — not implemented; field `user_devices.trusted_at` preserved for future use                            |

---

## Endpoints

All endpoints require `Authorization: Bearer <access_token>`.

### GET /auth/me/sessions

Returns the authenticated user's sessions, newest first. Active only by default.

**Query params:**

- `?include_revoked=true` — include revoked sessions
- `?limit=N` — number of sessions (default 50, max 100)

**Response fields:**

- `id` — session UUID
- `device_id` — linked device UUID or null
- `created_at`, `last_seen_at`, `idle_expires_at`, `absolute_expires_at` — RFC3339 timestamps
- `revoked_at` — null if active
- `ip_address` — masked (IPv4: `a.b.*.*`; IPv6: `prefix:*`)
- `user_agent` — truncated to 200 chars, non-printable stripped
- `current` — true if this is the session from the current access token

**Privacy:** Raw IP and user_agent are never returned. `refresh_token_hash`, `device_fingerprint_hash`,
and `password_hash` are never returned.

---

### DELETE /auth/me/sessions/{session_id}

Revokes a single session. Also revokes active `refresh_token_history` for that session.

- `204` — revoked (or own already-revoked session, idempotent)
- `400` — malformed UUID
- `404` — unknown or cross-user session

---

### DELETE /auth/me/sessions

Revokes all sessions except the current one.

- `204` — always on success
- `401` — access token is invalid/expired, lacks a `sid` claim, or the current DB session is revoked/expired/not owned by the token subject

**Current-session validation:** All `/auth/me/sessions` and `/auth/me/devices` routes first validate
the JWT `sid` against `auth.user_sessions` and `auth.users`. The current session must belong to the
JWT subject, have `revoked_at IS NULL`, have `idle_expires_at > now()`, have no expired
`absolute_expires_at`, and belong to an active, non-deleted user. Revoking the current session still
works because this validation happens before the revoke transaction.

---

### GET /auth/me/devices

Returns the authenticated user's linked devices, newest-last-seen first. Active only by default.

**Query params:**

- `?include_revoked=true` — include revoked devices
- `?limit=N` — default 50, max 100

**Response fields:**

- `id`, `display_name`, `platform`
- `last_ip` — masked
- `first_seen_at`, `last_seen_at`, `revoked_at`
- `session_count` — active sessions for this device
- `current` — true if the current access token's session belongs to this device
- **`meta.max_devices_per_user`** — from `auth_policy_settings`

---

### DELETE /auth/me/devices/{device_id}

Revokes the device, all its active sessions, and their active `refresh_token_history` rows in a
single CTE transaction.

- `204` — revoked (or own already-revoked device, idempotent)
- `400` — malformed UUID
- `404` — unknown or cross-user device

---

### PATCH /auth/me/devices/{device_id}

Updates `display_name` of an **active** device.

**Request body:** `{"display_name": "My Laptop"}`

- `204` — updated
- `400` — malformed UUID or invalid display_name (empty, >80 chars, or control-char-only)
- `404` — unknown, revoked, or cross-user device

**Validation:** `display_name` is trimmed, control characters (NUL, CR, LF, etc.) stripped,
1–80 chars enforced.

---

## Security Notes

- All device/session management routes require a valid access token with `sid` and an active
  database-backed current session; revoked, idle-expired, absolute-expired, cross-user, and
  missing-`sid` current sessions return `401`.
- All queries use `WHERE user_id = $authenticated_user_id` at SQL level — cross-user access is
  impossible.
- Parameterized SQL only — no string formatting in queries.
- IPs masked consistently with `/auth/me/login-attempts` endpoint.
- `device_fingerprint_hash`, `refresh_token_hash`, `password_hash`, raw tokens never returned or
  logged.

---

## Migration Rollout

Migration `000006_device_session_indexes` uses plain `CREATE INDEX` inside the repository-standard
transaction wrapper because `scripts/ci/migrations-check.sh` currently requires `BEGIN;` / `COMMIT;`
for auth migrations. This migration is intended for pre-production or not-yet-live auth tables. For
live production tables with existing write traffic, apply it during a maintenance window or replace
it in a future rollout with a non-transactional `CREATE INDEX CONCURRENTLY` migration path supported
by the migration framework.

---

## Out of Scope

- Frontend UI
- Admin RBAC for device management (future)
- RF-54: push/email notification for new device (future)
- MFA
- Hard-delete / anonymization of session/device records
- Device fingerprint enforcement for sessions without fingerprint
