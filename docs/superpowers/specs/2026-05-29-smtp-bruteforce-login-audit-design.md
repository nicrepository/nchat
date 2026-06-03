# Design: SMTP Transactional Delivery, Brute-Force Hardening, and Login Audit

**Date:** 2026-05-29  
**Branch:** `feat/auth-smtp-bruteforce-login-audit`  
**PR target:** `develop`  
**Requirements:** RF-35, RF-49, RF-50, RNF-25

---

## Scope

Three production-hardening tasks delivered in one PR:

- **Scope A** — Transactional SMTP delivery for `auth.email_outbox` (RF-35 / RNF-25)
- **Scope B** — Brute-force protection hardening (RF-49) — tests and docs only; implementation already complete
- **Scope C** — `GET /auth/me/login-attempts` endpoint (RF-50)

### Out of scope

- Full notification preference centre
- E-mail digest batching / DND / URGENT
- Final RBAC / admin views
- Frontend UI
- Real SMTP credentials in repo
- Valkey scheduler / general `notification_outbox` worker (deferred)

---

## Architecture overview

```
auth-service                         notification-service
─────────────────                    ───────────────────────────────────────
writes → auth.email_outbox           in-process ticker goroutine (SMTP worker)
         (AES-256-GCM payload)       ← SELECT FOR UPDATE SKIP LOCKED
                                     ← emailcrypto.Decrypt(payload)
GET /auth/me/login-attempts          renders template in memory
← BearerAuth middleware              → send via SMTP adapter
← LoginAttemptsService               ← mark sent_at / next_retry_at

libs/go/platform/emailcrypto
─────────────────────────────
EmailOutboxEncryptor { Encrypt, Decrypt }
(pure AES-256-GCM envelope, no domain knowledge)
imported by both services
```

---

## Scope A — Transactional SMTP

### Shared encryption library

**Package:** `libs/go/platform/emailcrypto`

Extracted from `auth-service/internal/service/email_outbox_encryption.go`. Purely cryptographic — no knowledge of password reset, invites, SMTP, or any domain concept.

Exports:

- `type Encryptor struct` with `Encrypt(plain Plaintext) (string, error)` and `Decrypt(envelopeJSON string) (Plaintext, error)`
- `type Plaintext struct { Kind, Token, LinkPath, ActionPath, ToEmail string; ExpiresAt time.Time }`
- `func New(base64Key string) (*Encryptor, error)` — fails fast on invalid key

Envelope format preserved exactly from PR #218:

```json
{ "alg": "AES-256-GCM", "key_version": "v1", "nonce": "<base64>", "ciphertext": "<base64>" }
```

`auth-service` removes its internal copy and imports `emailcrypto`. No behavioral change.

### notification-service config additions

New environment variables:

```
DATABASE_URL                     # PostgreSQL DSN (auth schema)
DB_CONNECT_TIMEOUT_SECONDS=5
AUTH_EMAIL_OUTBOX_ENCRYPTION_KEY # base64-encoded 32-byte key (same as auth-service)
AUTH_PUBLIC_WEB_BASE_URL         # e.g. https://app.nchat.example.com

SMTP_HOST                        # required when worker enabled
SMTP_PORT=587
SMTP_USERNAME
SMTP_PASSWORD
SMTP_FROM
SMTP_FROM_NAME=NChat
SMTP_TLS_MODE=starttls           # starttls | tls | none
SMTP_TIMEOUT_SECONDS=10
SMTP_MAX_ATTEMPTS=5
SMTP_BACKOFF_SECONDS=60
SMTP_WORKER_ENABLED=false        # default false; must be explicit true in prod
SMTP_WORKER_POLL_SECONDS=10
```

**Fail-fast**: if `SMTP_WORKER_ENABLED=true` and any required SMTP field is missing, the service starts but `/readyz` reports unhealthy (K8s will hold traffic and alert).

`SMTP_TLS_MODE=none` is only permitted when `APP_ENV` is `development`, `test`, or `local`. When `SMTP_WORKER_ENABLED=true` **and** `SMTP_TLS_MODE=none` **and** `APP_ENV` is anything else (e.g. `production`, `staging`), `/readyz` reports unhealthy — the service starts but will not serve traffic until misconfiguration is corrected.

### SMTP worker (`internal/worker/smtp_worker.go`)

#### Idempotency strategy

The worker uses a **short-claim + out-of-transaction send + finalise** pattern that avoids holding a long-lived database transaction during the SMTP call:

```
BEGIN                                                  ← claim transaction
  SELECT … FOR UPDATE SKIP LOCKED LIMIT 10
  UPDATE … SET status='processing',
               processing_started_at=now(),
               processing_deadline_at=now()+interval '$deadline s'
COMMIT                                                 ← release lock immediately

→ send via SMTP (outside any transaction)

BEGIN                                                  ← finalise transaction
  → success: UPDATE status='sent', sent_at=now()
  → failure: UPDATE attempts=attempts+1,
                    last_error=<safe diagnostic>,
                    next_retry_at=now()+(backoff*attempts*interval '1s'),
                    processing_started_at=NULL,
                    processing_deadline_at=NULL
                    status='pending'            (if attempts < max_attempts)
                    status='failed'             (if attempts >= max_attempts)
COMMIT
```

`processing_deadline_at` defaults to `now() + (2 * SMTP_TIMEOUT_SECONDS)`. The claim query reclaims rows where `status='processing'` **and** `processing_deadline_at <= now()`, so no startup sweep is needed. A restart or crash will automatically re-expose stale in-flight rows after the deadline passes.

**No double-send guarantee within a single process lifetime**: `FOR UPDATE SKIP LOCKED` prevents concurrent workers from claiming the same row, but a crash between SMTP success and the finalise `COMMIT` may re-send the row after the deadline. This is the standard at-least-once outbox trade-off at MVP level.

#### Worker loop

```
Worker.Start(ctx) → ticker loop every SMTP_WORKER_POLL_SECONDS
  └── claimBatch()
      └── for each row:
            decrypt(payload)         → in memory only; never logged
            renderTemplate(kind)     → text + HTML; token in memory only
            smtp.Send(…)
            → success: finalise as 'sent'
            → failure: finalise as 'pending' (or 'failed' if attempts >= max)
```

**Claim query:**

```sql
SELECT id, kind, to_email, subject, template_key, payload, attempts
FROM auth.email_outbox
WHERE (
    (status = 'pending'
     AND (next_retry_at IS NULL OR next_retry_at <= now()))
  OR
    (status = 'processing'
     AND processing_deadline_at <= now())
)
  AND attempts < $max_attempts
ORDER BY next_retry_at NULLS FIRST, created_at, id
FOR UPDATE SKIP LOCKED
LIMIT 10
```

The `attempts < $max_attempts` guard prevents re-claiming an exhausted row in the unlikely event the finalise transaction failed to write `status='failed'`. Rows that have been finalized to `status='failed'` are never re-claimed.

**Security invariants:**

- Decrypted payload lives only in Go memory during processing
- `last_error` stores safe diagnostic strings (e.g. "smtp timeout", "tls handshake failed") — never the token, link, or decrypted payload
- SMTP credentials come only from env/secrets; never logged

### Email templates

Two minimal templates (plain text + HTML):

| Kind             | Subject                      | Variables                                                         |
| ---------------- | ---------------------------- | ----------------------------------------------------------------- |
| `password_reset` | Reset your NChat password    | `ResetLink` (`AUTH_PUBLIC_WEB_BASE_URL + link_path`), `ExpiresAt` |
| `invite`         | You've been invited to NChat | `AcceptLink`, `ExpiresAt`                                         |

Templates rendered in memory. Rendered body with token is never persisted. `ResetLink` / `AcceptLink` are constructed from `AUTH_PUBLIC_WEB_BASE_URL + plaintext.LinkPath`.

### SMTP adapter interface

```go
type Sender interface {
    Send(ctx context.Context, msg Message) error
}

type Message struct {
    From, FromName, To, Subject, TextBody, HTMLBody string
}
```

Real implementation: `net/smtp` with TLS/STARTTLS/plain switch.  
Fake implementation: `FakeSender` for tests — records sent messages, never dials network.

### Local dev SMTP

`infra/compose/compose.dev.yml` exists but does not use profiles, so Mailpit is provided via `docker run`:

```bash
docker run -d --name mailpit -p 1025:1025 -p 8025:8025 axllent/mailpit
# SMTP_HOST=localhost SMTP_PORT=1025 SMTP_TLS_MODE=none APP_ENV=development
# UI: http://localhost:8025
```

---

## Scope B — Brute-force hardening

**Status: implementation complete.** No code changes required.

Verified against all spec requirements:

| Requirement                            | Implementation                                 | Test                                       |
| -------------------------------------- | ---------------------------------------------- | ------------------------------------------ |
| Configurable limit / window / lockout  | `auth.auth_policy_settings`                    | `policyRow(...)`                           |
| Temporary lockout (not `users.status`) | `loginTemporarilyLocked()`                     | `TemporaryLockout`                         |
| Lockout expiry                         | time window math in `loginTemporarilyLocked()` | `LockoutExpiredAfterLockoutMinutes`        |
| Only credential failures count         | `credentialFilter` const                       | `NonCredentialFailuresDoNotTriggerLockout` |
| Concurrency guard                      | `pg_advisory_xact_lock` per email hash         | `LoginLockAcquireError`                    |
| Unknown email path generic             | `dummyVerify` + same error                     | `UnknownEmail`                             |
| `users.status` not mutated             | no `UPDATE users` in login failure path        | code review                                |

**Additions in this PR:**

- Service-layer tests with RF-49 traceability comments
- Inline doc comments linking `loginTemporarilyLocked` to RF-49 / RNF-25

---

## Scope C — `GET /auth/me/login-attempts`

### Route

```
GET /auth/me/login-attempts?limit=50&cursor=<base64>
Authorization: Bearer <access_token>
```

### Bearer middleware

`BearerAuth(tokens *service.TokenManager) func(http.Handler) http.Handler`

- Extracts `Authorization: Bearer <token>`
- Calls `tokens.ValidateAccessToken()`; returns 401 on missing/invalid/expired
- Injects `userID` (from `claims.Subject`) into `context.WithValue`
- Returns generic `{"error":"unauthorized"}` — no token detail in response

### Store

`PGXLoginAttemptsStore.GetUserFailedAttempts(ctx, userID string, limit int, cursor *LoginAttemptsCursor)`:

```sql
SELECT id, email, success, failure_reason, ip_address, user_agent, created_at
FROM auth.login_attempts
WHERE user_id = $1
  AND success = false
  AND ($cursor_created_at IS NULL
       OR (created_at, id) < ($cursor_created_at, $cursor_id))
ORDER BY created_at DESC, id DESC
LIMIT $limit + 1
```

Uses partial index `idx_login_attempts_user_failed_created_id`.

Fetching `limit+1` rows detects whether a next page exists. If `len(rows) == limit+1`, strip the last row and encode `nextCursor` from `rows[limit-1].{created_at, id}`.

### Cursor encoding

`LoginAttemptsCursor { CreatedAt time.Time; ID int64 }` — `id` is `BIGSERIAL` (stored as `int64`) per `migrations/auth/000001_auth_identity_schema.up.sql`. Encoded as base64(JSON). Invalid cursor → 400.

### Service

`LoginAttemptsService.GetMyAttempts(ctx, userID, limit int, cursor string)`:

- Clamps `limit` to `[1, 100]`; default 50
- Returns `([]domain.LoginAttempt, nextCursor string, error)`

### Response shape

```json
{
  "data": [
    {
      "id": "123",
      "email": "user@example.com",
      "ip": "203.0.*.*",
      "user_agent": "Mozilla/5.0 (truncated to 200 chars)",
      "failure_reason": "invalid_credentials",
      "created_at": "2026-05-29T10:00:00Z"
    }
  ],
  "pagination": {
    "limit": 50,
    "next_cursor": null
  }
}
```

### IP masking

- IPv4: replace last two octets with `*.*` → `203.0.*.*`
- IPv6: retain first 32-bit group only → `2001:db8:*:*:*:*:*:*`
- nil / unparseable: omit field from response
- Never expose raw headers

### Privacy guarantees

- `email` field: only the authenticated user's own email — normalised, not masked
- No password, token, session ID, or device fingerprint in response
- Only `success=false` rows returned
- `user_agent` truncated to 200 chars, control characters stripped

---

## Migration 005

**File:** `migrations/auth/000005_smtp_worker_login_audit.up.sql`

```sql
BEGIN;
SET LOCAL search_path = auth, public;

-- SMTP worker: add processing state and scheduling columns
ALTER TABLE auth.email_outbox
    DROP CONSTRAINT email_outbox_status_check,
    ADD CONSTRAINT email_outbox_status_check
        CHECK (status IN ('pending', 'processing', 'sent', 'failed'));

ALTER TABLE auth.email_outbox
    ADD COLUMN next_retry_at          TIMESTAMPTZ,
    ADD COLUMN processing_started_at  TIMESTAMPTZ,
    ADD COLUMN processing_deadline_at TIMESTAMPTZ;

-- Covers pending rows ready for retry AND expired processing rows (deadline passed)
CREATE INDEX idx_email_outbox_claimable
    ON auth.email_outbox (next_retry_at NULLS FIRST, created_at, id)
    WHERE status IN ('pending', 'processing');

-- Efficient cursor pagination for GET /auth/me/login-attempts
CREATE INDEX idx_login_attempts_user_failed_created_id
    ON auth.login_attempts (user_id, created_at DESC, id DESC)
    WHERE success = false;

COMMIT;
```

**Down:**

```sql
BEGIN;
SET LOCAL search_path = auth, public;

DROP INDEX IF EXISTS auth.idx_login_attempts_user_failed_created_id;
DROP INDEX IF EXISTS auth.idx_email_outbox_claimable;

-- Reset any processing rows before dropping the status constraint
UPDATE auth.email_outbox SET status = 'pending' WHERE status = 'processing';

ALTER TABLE auth.email_outbox
    DROP COLUMN IF EXISTS processing_deadline_at,
    DROP COLUMN IF EXISTS processing_started_at,
    DROP COLUMN IF EXISTS next_retry_at;

ALTER TABLE auth.email_outbox
    DROP CONSTRAINT email_outbox_status_check,
    ADD CONSTRAINT email_outbox_status_check
        CHECK (status IN ('pending', 'sent', 'failed'));

COMMIT;
```

---

## K8s / secrets

`nchat-secrets` template additions:

```yaml
SMTP_HOST: "CHANGE_ME_smtp_host"
SMTP_PORT: "587"
SMTP_USERNAME: "CHANGE_ME_smtp_username"
SMTP_PASSWORD: ""
SMTP_FROM: "CHANGE_ME_smtp_from@example.com"
SMTP_FROM_NAME: "NChat"
AUTH_EMAIL_OUTBOX_ENCRYPTION_KEY: "" # same key as auth-service; generate: openssl rand -base64 32
```

notification-service K8s deployment: SMTP vars via `secretKeyRef`. `SMTP_WORKER_ENABLED` in ConfigMap, defaults `"false"`.

---

## Tests

### emailcrypto (libs)

- Round-trip: encrypt then decrypt returns original plaintext
- Wrong key → decrypt error
- Tampered ciphertext → decrypt error
- Invalid base64 key → New() error

### notification-service SMTP worker

- `SMTP_WORKER_ENABLED=false` → worker not started, service healthy
- `SMTP_WORKER_ENABLED=true` + missing SMTP config → readyz unhealthy
- `SMTP_WORKER_ENABLED=true` + `SMTP_TLS_MODE=none` + `APP_ENV=production` → readyz unhealthy
- `SMTP_WORKER_ENABLED=true` + `SMTP_TLS_MODE=none` + `APP_ENV=development` → allowed, service healthy
- `SMTP_WORKER_ENABLED=true` + valid config + FakeSender → pending row claimed, rendered, sent, marked `sent`
- FakeSender returns error → attempts incremented, `next_retry_at` set, `processing_*` columns cleared, status `pending`
- `attempts >= SMTP_MAX_ATTEMPTS` → status `failed`
- Row with `attempts >= SMTP_MAX_ATTEMPTS` not included in claim query (`AND attempts < $max_attempts`)
- `status='processing'` row with expired `processing_deadline_at` is re-claimed by next poll
- Decrypted token appears only in FakeSender.Send() call — never in DB or logs
- No plaintext token in `auth.email_outbox.payload` (ciphertext stored)

### auth-service — brute-force (service layer)

- RF-49: policy limit=1 → first credential failure locks
- RF-49: policy limit=0 → lockout disabled
- Traceability comments linking to RF-49

### auth-service — login attempts endpoint

- No Bearer → 401
- Invalid/expired Bearer → 401
- Valid Bearer → returns only own `success=false` rows
- `limit` clamped at 100
- `next_cursor` present when more rows exist
- Response contains no password / token / hash
- `ip` field masked (last two octets replaced)
- `user_agent` truncated to 200 chars

---

## RF/RNF traceability

| Requirement | Status                   | Delivered by                                                                                                                                                                                                            |
| ----------- | ------------------------ | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| RF-35       | **Partial / foundation** | `emailcrypto` lib + notification-service SMTP worker for `auth.email_outbox`. Full notification delivery (preferences, digest, other channels, general `notification_outbox`) is out of scope for this PR.              |
| RF-49       | **Complete**             | `loginTemporarilyLocked` + `auth.auth_policy_settings` (existing, PR #216/#218)                                                                                                                                         |
| RF-50       | **Complete**             | `GET /auth/me/login-attempts`                                                                                                                                                                                           |
| RNF-25      | **Partial / foundation** | PostgreSQL outbox as source of truth + polling worker + retry/backoff for `auth.email_outbox`. Valkey scheduler/worker pattern and general `notification_outbox` handling are out of scope — deferred to a future task. |

---

## Files changed (summary)

```
libs/go/platform/emailcrypto/         NEW — shared AES-256-GCM lib
migrations/auth/000005_*.{up,down}.sql NEW — next_retry_at + indexes
services/auth-service/
  internal/service/email_outbox_encryption.go  DELETED (moved to lib)
  internal/service/*                  import emailcrypto
  internal/http/routes.go             + RouteAuthMeLoginAttempts
  internal/http/login_attempts_handler.go  NEW
  internal/http/bearer_middleware.go  NEW
  internal/service/login_attempts_service.go NEW
  internal/storage/login_attempts_store.go   NEW
  internal/app/app.go                 wire new components
  internal/config/config.go           no change (no new env vars needed)
services/notification-service/
  internal/config/config.go           + SMTP / DB / encryption vars
  internal/worker/smtp_worker.go      NEW
  internal/worker/smtp_sender.go      NEW (interface + real + fake)
  internal/worker/templates.go        NEW (password_reset + invite)
  internal/app/app.go                 wire DB + worker
  .env.example                        + SMTP vars
infra/k8s/base/services/notification-service/deployment.yaml  SMTP secretKeyRef
infra/k8s/secrets/templates/nchat-secrets.template.yaml       + SMTP placeholders
docs/runbooks/task-smtp-bruteforce-login-audit.md  NEW
README.md                             auth + notification sections
```
