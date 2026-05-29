# Runbook: SMTP Transactional Delivery, Brute-Force Hardening, and Login Audit

## Overview

This runbook covers RF-35 (partial), RF-49 (complete), RF-50 (complete), and
RNF-25 (partial) for auth-service and notification-service.

| RF     | Requirement                                      | Status             | Implementation                                                                      |
| ------ | ------------------------------------------------ | ------------------ | ----------------------------------------------------------------------------------- |
| RF-35  | Envio de e-mails transacionais                   | Partial/Foundation | `notification-service` SMTP worker — opt-in via `SMTP_WORKER_ENABLED=true`          |
| RF-49  | Proteção contra força bruta no login             | Complete           | DB-resident lockout policy + new test coverage for all lockout paths                |
| RF-50  | Log de tentativas falhas de login                | Complete           | `GET /auth/me/login-attempts` — authenticated user can query own failed attempts    |
| RNF-25 | Segurança de credenciais no transporte de e-mail | Partial/Foundation | TLS enforcement for SMTP; `TLSMode=none` blocked in non-dev/test/local environments |

---

## Endpoints

### `GET /auth/me/login-attempts`

Returns a paginated list of the authenticated user's **failed** login attempts.

**Auth:** `Authorization: Bearer <access_token>` (HS256 JWT issued by auth-service)

**Query parameters:**

| Parameter | Type    | Default | Description                       |
| --------- | ------- | ------- | --------------------------------- |
| `limit`   | integer | `50`    | Page size — clamped to `[1, 100]` |
| `cursor`  | string  | —       | Opaque base64 pagination cursor   |

**Response `200`:**

```json
{
  "data": [
    {
      "id": "12345",
      "email": "user@example.com",
      "ip_address": "1.2.*.*",
      "user_agent": "Mozilla/5.0 ...",
      "failure_reason": "invalid_credentials",
      "created_at": "2026-05-29T14:00:00Z"
    }
  ],
  "pagination": {
    "limit": 50,
    "next_cursor": null
  }
}
```

**Privacy notes:**

- `id` is returned as a string (avoids JavaScript integer overflow).
- IPv4: last two octets masked (`1.2.3.4` → `1.2.*.*`).
- IPv6: all but first group masked (`2001:db8::1` → `2001:*`).
- `user_agent` truncated to 200 chars; non-printable chars stripped.
- Only `success = false` rows are returned (brute-force audit, not full session history).

**Error responses:**

| Status | Code                  | Reason                                      |
| ------ | --------------------- | ------------------------------------------- |
| `400`  | `bad_request`         | Invalid cursor encoding or format           |
| `401`  | `unauthorized`        | Missing, malformed, or expired Bearer token |
| `503`  | `service_unavailable` | Database not configured                     |

---

## SMTP Worker

### Configuration variables

| Variable                           | Default    | Secret  | Description                                                  |
| ---------------------------------- | ---------- | ------- | ------------------------------------------------------------ |
| `SMTP_HOST`                        | `""`       | No      | SMTP server hostname                                         |
| `SMTP_PORT`                        | `587`      | No      | SMTP server port                                             |
| `SMTP_USERNAME`                    | `""`       | No      | SMTP authentication username                                 |
| `SMTP_PASSWORD`                    | `""`       | **Yes** | SMTP authentication password (store in Sealed Secret)        |
| `SMTP_FROM`                        | `""`       | No      | Envelope `From` address                                      |
| `SMTP_FROM_NAME`                   | `NChat`    | No      | Display name for the `From` header                           |
| `SMTP_TLS_MODE`                    | `starttls` | No      | `starttls` (default), `tls`, or `none` (dev/test/local only) |
| `SMTP_TIMEOUT_SECONDS`             | `10`       | No      | Per-connection dial timeout                                  |
| `SMTP_MAX_ATTEMPTS`                | `5`        | No      | Max delivery attempts before marking `failed`                |
| `SMTP_BACKOFF_SECONDS`             | `60`       | No      | Retry backoff (seconds) after each failure                   |
| `SMTP_WORKER_ENABLED`              | `false`    | No      | Master switch — worker does not start unless `true`          |
| `SMTP_WORKER_POLL_SECONDS`         | `10`       | No      | Outbox poll interval (seconds)                               |
| `AUTH_EMAIL_OUTBOX_ENCRYPTION_KEY` | `""`       | **Yes** | 32-byte AES key (base64) — must match auth-service           |
| `AUTH_PUBLIC_WEB_BASE_URL`         | `""`       | No      | Base URL used to construct reset/invite links                |
| `DATABASE_URL`                     | `""`       | **Yes** | PostgreSQL connection string for outbox polling              |

`SMTP_WORKER_ENABLED` defaults to `false` in the ConfigMap. To enable SMTP
delivery, set it to `true` in the environment overlay (overlays or Sealed Secret)
and provide all required SMTP vars.

### TLS requirements

`SMTP_TLS_MODE=none` is **only** permitted when `APP_ENV` is `development`,
`test`, or `local`. In any other environment (`production`, `staging`, etc.),
the worker will refuse to start if `TLSMode=none`.

### Enabling SMTP delivery

1. Rotate `AUTH_EMAIL_OUTBOX_ENCRYPTION_KEY` and ensure it matches the key used
   by auth-service.
2. Seal `SMTP_PASSWORD` and `AUTH_EMAIL_OUTBOX_ENCRYPTION_KEY` via Sealed
   Secrets (see [sealed-secrets-rotation.md](sealed-secrets-rotation.md)).
3. Set `SMTP_WORKER_ENABLED: "true"` in the environment ConfigMap or overlay.
4. Verify `readyz` health check passes — `smtp-worker-config` checker will
   return `fail` if any required var is missing.

### Local SMTP smoke test with Mailpit

```bash
# Start a local Mailpit SMTP+UI container (no profile required)
docker run -d --name mailpit -p 1025:1025 -p 8025:8025 axllent/mailpit

# Point notification-service at Mailpit
SMTP_HOST=localhost SMTP_PORT=1025 SMTP_TLS_MODE=none SMTP_WORKER_ENABLED=true \
  ./notification-service

# Open http://localhost:8025 to inspect delivered emails
```

> **Note:** `infra/compose/compose.dev.yml` does not include a Mailpit profile.
> Run it standalone as shown above, or add it to your local compose file.

---

## Brute-Force Lockout (RF-49)

Lockout is entirely DB-resident and does **not** mutate `auth.users.status`.
The policy is stored in `auth.auth_policy_settings`:

| Column                         | Default | Description                                                |
| ------------------------------ | ------- | ---------------------------------------------------------- |
| `failed_login_limit`           | `5`     | Failures within the window before lockout                  |
| `failed_login_window_minutes`  | `15`    | Rolling window for counting `invalid_credentials` failures |
| `failed_login_lockout_minutes` | `15`    | Lockout duration after threshold is crossed                |

Only `failure_reason = 'invalid_credentials'` rows count towards lockout.
`failed_login_limit_exceeded`, `device_revoked`, and `max_devices_exceeded`
rows are explicitly excluded.

---

## Sealed Secrets / `secretKeyRef` requirements

The following keys must be present in the `nchat-secrets` Sealed Secret:

| Key                                | Notes                                           |
| ---------------------------------- | ----------------------------------------------- |
| `SMTP_PASSWORD`                    | SMTP password; use `""` placeholder until ready |
| `AUTH_EMAIL_OUTBOX_ENCRYPTION_KEY` | 32-byte base64 AES key; must match auth-service |
| `DATABASE_URL`                     | Already present                                 |

The notification-service deployment uses `secretKeyRef optional: true` for
`SMTP_PASSWORD` and `AUTH_EMAIL_OUTBOX_ENCRYPTION_KEY` so missing keys do not
crash the pod.

---

## Security notes

- **No credentials in the repo.** `SMTP_PASSWORD` and `AUTH_EMAIL_OUTBOX_ENCRYPTION_KEY`
  must be managed via Sealed Secrets — never committed.
- **TLS enforcement.** `SMTP_TLS_MODE=none` is blocked in non-dev environments.
- **In-memory token handling.** Decrypted plaintext (including the raw token) is
  used only to construct the email body and never written to logs, `last_error`,
  or the database.
- **At-least-once delivery.** A crash between `sender.Send` succeeding and
  `FinaliseSuccess` writing to the DB may cause a duplicate email on the next
  poll cycle. This is a known limitation; idempotent delivery is out of scope.

---

## Database migration

Migration `000005_smtp_worker_login_audit.{up,down}.sql` covers:

- `email_outbox` status extended to include `'processing'`
- `next_retry_at`, `processing_started_at`, `processing_deadline_at` columns
- `idx_email_outbox_claimable` index for efficient poll queries
- `idx_login_attempts_user_failed_created_id` index for login audit queries

---

## Out of scope

- Notification preference centre, digest batching, DND/URGENT scheduling
- Final RBAC (endpoint currently requires only a valid access token)
- Frontend UI for login attempts
- Valkey-based scheduler or general `notification_outbox` table
- Admin view of all users' login attempts

---

## Known limitation

**At-least-once SMTP delivery.** If the process crashes after sending the email
but before committing `status='sent'`, the outbox row is reclaimed on the next
poll and the email is sent again. This is acceptable for transactional auth
emails (password reset, invite) which are already idempotent from the user
perspective (the link contains a token that is only valid once).

---

## Architecture note: cross-schema coupling

notification-service reads from `auth.email_outbox`. This is an accepted MVP
technical compromise documented here for future maintainers.

**Why it exists:** RF-35 and RNF-25 require PostgreSQL-backed outbox for auth
transactional emails (password reset, invite). The notification-service owns
email delivery, so it polls `auth.email_outbox` directly.

**Scope:** Limited to auth transactional emails only. No general notification
routing crosses this boundary.

**Future direction (out of scope for this PR):**

- Migrate to a shared `notification_outbox` table owned by notification-service,
  or adopt CDC (Change Data Capture) with a Valkey-based scheduler when the
  notification platform expands beyond auth transactional emails.
- Do NOT move the SMTP worker into auth-service as an interim step.

This coupling is intentional and bounded. It must not be expanded to include
non-auth schemas or general notification routing without a dedicated RFC.
