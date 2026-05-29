# SMTP Transactional Delivery, Brute-Force Hardening, and Login Audit — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Spec:** `docs/superpowers/specs/2026-05-29-smtp-bruteforce-login-audit-design.md`  
**Branch:** `feat/auth-smtp-bruteforce-login-audit` (base: `develop`)  
**PR target:** `develop`  
**Requirements:** RF-35 (partial), RF-49 (complete), RF-50 (complete), RNF-25 (partial)

---

### Task 1: Feature Branch and Migration 005

**Files:**

- Create: `migrations/auth/000005_smtp_worker_login_audit.up.sql`
- Create: `migrations/auth/000005_smtp_worker_login_audit.down.sql`

- [ ] **Step 1: Create feature branch**

  ```bash
  git checkout develop && git pull origin develop
  git checkout -b feat/auth-smtp-bruteforce-login-audit
  ```

- [ ] **Step 2: Write up migration**

  Create `000005_smtp_worker_login_audit.up.sql` with `BEGIN`, `SET LOCAL search_path = auth, public`:
  - `DROP CONSTRAINT email_outbox_status_check` then re-add with `('pending','processing','sent','failed')`
  - `ADD COLUMN next_retry_at TIMESTAMPTZ`
  - `ADD COLUMN processing_started_at TIMESTAMPTZ`
  - `ADD COLUMN processing_deadline_at TIMESTAMPTZ`
  - `CREATE INDEX idx_email_outbox_claimable ON auth.email_outbox (next_retry_at NULLS FIRST, created_at, id) WHERE status IN ('pending', 'processing')`
  - `CREATE INDEX idx_login_attempts_user_failed_created_id ON auth.login_attempts (user_id, created_at DESC, id DESC) WHERE success = false`
  - `COMMIT`

- [ ] **Step 3: Write down migration**

  Create `000005_smtp_worker_login_audit.down.sql`:
  - `BEGIN`, `SET LOCAL search_path = auth, public`
  - Drop both indexes first
  - `UPDATE auth.email_outbox SET status='pending' WHERE status='processing'`
  - `DROP COLUMN IF EXISTS processing_deadline_at`, `processing_started_at`, `next_retry_at`
  - Restore original `status IN ('pending','sent','failed')` constraint
  - `COMMIT`

- [ ] **Step 4: Validate migration syntax**

  ```bash
  bash -n scripts/db/migrate.sh scripts/ci/migrations-check.sh
  pnpm migrations:check
  ```

  Expected: exit 0.

---

### Task 2: `libs/go/platform/emailcrypto` shared library

**Files:**

- Create: `libs/go/platform/emailcrypto/emailcrypto.go`
- Create: `libs/go/platform/emailcrypto/emailcrypto_test.go`
- Create: `libs/go/platform/emailcrypto/doc.go`
- Modify: `libs/go/platform/go.mod` (add package declaration if needed)

- [ ] **Step 1: Create emailcrypto package**

  Extract `EmailOutboxEncryptor` + `EmailOutboxPlaintext` from `auth-service/internal/service/email_outbox_encryption.go` into `libs/go/platform/emailcrypto`. Rename: `EmailOutboxEncryptor` → `Encryptor`, `EmailOutboxPlaintext` → `Plaintext`, `NewEmailOutboxEncryptor` → `New`. Preserve envelope format and AAD exactly (`"AES-256-GCM:v1"`). Remove domain imports — `ErrEmailOutboxUnavailable` becomes a package-local sentinel.

- [ ] **Step 2: Write emailcrypto tests**

  Cover: round-trip encrypt→decrypt returns original plaintext; wrong key returns decrypt error; tampered ciphertext returns decrypt error; invalid base64 key returns `New()` error; `nil` encryptor returns error.

- [ ] **Step 3: Run emailcrypto tests**

  ```bash
  cd libs/go/platform && go test -count=1 ./emailcrypto/...
  ```

  Expected: pass.

---

### Task 3: auth-service — use shared emailcrypto lib

**Files:**

- Delete: `services/auth-service/internal/service/email_outbox_encryption.go`
- Delete: `services/auth-service/internal/service/email_outbox_encryption_test.go`
- Modify: `services/auth-service/internal/service/password_reset_service.go`
- Modify: `services/auth-service/internal/service/invite_service.go`
- Modify: `services/auth-service/internal/app/app.go`
- Modify: `services/auth-service/internal/domain/errors.go`
- Modify: `services/auth-service/go.mod`

- [ ] **Step 1: Update go.mod to depend on emailcrypto**

  Add `github.com/nicrepository/nchat/libs/go/platform/emailcrypto` replace directive (workspace-style, same as existing `libs/go/platform` reference). Run `go mod tidy`.

- [ ] **Step 2: Replace internal encryptor with lib import**

  In `password_reset_service.go` and `invite_service.go`, replace all references to `service.EmailOutboxEncryptor` / `service.EmailOutboxPlaintext` / `service.NewEmailOutboxEncryptor` with `emailcrypto.Encryptor` / `emailcrypto.Plaintext` / `emailcrypto.New`.

  In `app.go`, update `service.NewEmailOutboxEncryptor(...)` → `emailcrypto.New(...)`.

  Remove `ErrEmailOutboxUnavailable` from `domain/errors.go` if it is now unused; otherwise keep it as a domain-level alias wrapping the lib error.

- [ ] **Step 3: Delete the internal copy**

  Delete `email_outbox_encryption.go` and `email_outbox_encryption_test.go`.

- [ ] **Step 4: Verify auth-service still passes**

  ```bash
  cd services/auth-service && go test -count=1 ./...
  ```

  Expected: pass.

---

### Task 4: notification-service — config, DB, and SMTP adapter

**Files:**

- Modify: `services/notification-service/internal/config/config.go`
- Modify: `services/notification-service/internal/config/config_test.go`
- Modify: `services/notification-service/.env.example`
- Modify: `services/notification-service/go.mod`
- Create: `services/notification-service/internal/worker/smtp_sender.go`
- Create: `services/notification-service/internal/worker/smtp_sender_test.go`

- [ ] **Step 1: Extend config**

  Add to `Config`:

  ```go
  DatabaseURL              string
  DBConnectTimeoutSeconds  int
  AuthEmailOutboxEncKey    string   // AUTH_EMAIL_OUTBOX_ENCRYPTION_KEY
  AuthPublicWebBaseURL     string   // AUTH_PUBLIC_WEB_BASE_URL
  SMTPHost                 string
  SMTPPort                 int      // default 587
  SMTPUsername             string
  SMTPPassword             string
  SMTPFrom                 string
  SMTPFromName             string   // default "NChat"
  SMTPTLSMode              string   // default "starttls"
  SMTPTimeoutSeconds       int      // default 10
  SMTPMaxAttempts          int      // default 5
  SMTPBackoffSeconds       int      // default 60
  SMTPWorkerEnabled        bool     // default false
  SMTPWorkerPollSeconds    int      // default 10
  ```

  Add `SMTPWorkerReady(env string) (bool, string)` helper that returns `(false, reason)` when worker is enabled but config is incomplete or `TLSMode=none` in non-dev/test/local env.

- [ ] **Step 2: Write config tests**

  Cover: worker disabled → `SMTPWorkerReady` returns true (no check); worker enabled + missing host → false; worker enabled + `TLSMode=none` + `APP_ENV=production` → false; worker enabled + `TLSMode=none` + `APP_ENV=development` → true; worker enabled + full config + `TLSMode=starttls` → true.

- [ ] **Step 3: Update .env.example**

  Add all SMTP vars with `""` for password/key fields and obvious `CHANGE_ME_*` for non-secret fields. Comment `SMTP_WORKER_ENABLED=false`.

- [ ] **Step 4: Add go.mod dependencies**

  Run `go get github.com/jackc/pgx/v5` (for DB polling). `net/smtp` and `crypto/tls` are stdlib — no external SMTP lib needed.

- [ ] **Step 5: Create SMTP sender interface + real + fake**

  `internal/worker/smtp_sender.go`:

  ```go
  type Sender interface {
      Send(ctx context.Context, msg Message) error
  }
  type Message struct {
      From, FromName, To, Subject, TextBody, HTMLBody string
  }
  ```

  `NetSMTPSender`: dials `SMTP_HOST:SMTP_PORT`, branches on `TLSMode` — `"tls"` uses `tls.Dial`, `"starttls"` uses `smtp.Dial` then `STARTTLS`, `"none"` uses plain `smtp.Dial`. Respects `SMTP_TIMEOUT_SECONDS` via context deadline. `FakeSender`: records `[]Message`, never dials. Returns configurable error if `ErrToReturn != nil`.

- [ ] **Step 6: Write sender tests**

  Cover `FakeSender`: records message, returns error when configured. Real sender: `TLSMode` branches compile and select correct dialing path (unit-testable without network).

---

### Task 5: notification-service — email templates

**Files:**

- Create: `services/notification-service/internal/worker/templates.go`
- Create: `services/notification-service/internal/worker/templates_test.go`

- [ ] **Step 1: Create template renderer**

  Two `text/template` + `html/template` pairs (plain text + HTML) for kinds `password_reset` and `invite`. Input: `TemplateData { ResetLink/AcceptLink string; ExpiresAt time.Time }`. Link constructed as `baseURL + plaintext.LinkPath`. Return `(textBody, htmlBody string, error)`. Never persist rendered body. Template strings are constants in Go — no file embedding.

  Minimal plain-text template:
  - `password_reset`: "Reset your NChat password by visiting: {{.ResetLink}}\nThis link expires at {{.ExpiresAt}}."
  - `invite`: "You've been invited to NChat. Accept at: {{.AcceptLink}}\nThis link expires at {{.ExpiresAt}}."

  HTML templates: same content wrapped in minimal `<html><body>` with safe `<a href>`.

- [ ] **Step 2: Write template tests**

  Cover: password_reset renders link from baseURL+linkPath; invite renders accept link; rendered text does not contain the raw `Token` field from plaintext (link is constructed, token is embedded in URL only via `linkPath`); unknown kind returns error.

---

### Task 6: notification-service — SMTP worker and app wiring

**Files:**

- Create: `services/notification-service/internal/worker/smtp_worker.go`
- Create: `services/notification-service/internal/worker/smtp_worker_test.go`
- Create: `services/notification-service/internal/storage/outbox_store.go`
- Create: `services/notification-service/internal/storage/pool.go`
- Modify: `services/notification-service/internal/app/app.go`
- Modify: `services/notification-service/internal/app/app_test.go`
- Modify: `services/notification-service/internal/http/handlers.go`

- [ ] **Step 1: Create outbox store**

  `PGXOutboxStore` with two methods:
  - `ClaimBatch(ctx, maxAttempts, deadlineSeconds, batchSize int) ([]OutboxRow, error)`:
    BEGIN; SELECT + FOR UPDATE SKIP LOCKED (pending or expired processing, attempts < max); UPDATE status='processing', processing_started_at=now(), processing_deadline_at=now()+interval; COMMIT.
  - `FinaliseSuccess(ctx, id UUID) error`: UPDATE status='sent', sent_at=now(), processing_started_at=NULL, processing_deadline_at=NULL.
  - `FinaliseFailure(ctx, id UUID, attempts int, lastError string, backoffSec int, maxAttempts int) error`: UPDATE attempts, last_error, next_retry_at, clear processing columns; if attempts >= max → status='failed', else status='pending'.

- [ ] **Step 2: Create SMTP worker**

  `Worker { cfg config.Config; store OutboxStore; decryptor *emailcrypto.Encryptor; sender Sender }`.

  `Start(ctx)`: ticker every `SMTPWorkerPollSeconds`. Each tick: `claimBatch` → for each row: decrypt payload (in-memory) → renderTemplate → `sender.Send` → finalise. Log only safe diagnostics (no token, link, password, decrypted payload). On decrypt error: finalise as failure with `last_error="decrypt_error"`.

- [ ] **Step 3: Write worker tests**

  Use `FakeSender` and pgxmock (or a `FakeOutboxStore` interface double):
  - Worker disabled → `Start` not called
  - Pending row claimed → decrypted → rendered → `FakeSender.Send` called with subject and non-empty body → `FinaliseSuccess` called
  - `FakeSender` returns error → `FinaliseFailure` called with incremented attempts, `next_retry_at` set
  - `attempts >= max` → `FinaliseFailure` sets status `failed`
  - Expired processing row reclaimed by next poll
  - Decrypted `Token` field appears only in `FakeSender.Sent[0]` body (not in `last_error`, not stored back to DB)
  - Payload in DB is ciphertext (round-trip: encrypt first, then run worker)

- [ ] **Step 4: Update readyz handler**

  In `handlers.go`, add `smtpWorkerCheck(cfg config.Config) health.Checker`:
  - if worker disabled → `health.CheckPass`
  - if `SMTPWorkerReady` returns false → `health.CheckFail` with reason

- [ ] **Step 5: Wire in app.go**

  In `app.New`: open DB if `DatabaseURL != ""`; create `emailcrypto.New(cfg.AuthEmailOutboxEncKey)`; if worker enabled and config ready, start worker goroutine with `go worker.Start(ctx)`; update `Readyz` checker list.

- [ ] **Step 6: Run notification-service tests**

  ```bash
  cd services/notification-service && go test -count=1 ./...
  ```

  Expected: pass.

---

### Task 7: auth-service — brute-force tests (Scope B)

**Files:**

- Modify: `services/auth-service/internal/service/login_service_test.go`
- Modify: `services/auth-service/internal/storage/login_store_test.go`

- [ ] **Step 1: Add service-layer brute-force tests**

  In `login_service_test.go`, add tests with RF-49 traceability comments:
  - `RF49_PolicyLimitOne_FirstCredentialFailureLocks`: store returns `ErrInvalidCredentials`; service propagates it without branching on lockout (lockout is in store)
  - `RF49_PolicyLimitZero_LockoutDisabled`: not tested at service layer (policy is DB-resident); add comment linking to storage test

- [ ] **Step 2: Add storage-layer brute-force tests**

  In `login_store_test.go`, using pgxmock, add:
  - `RF49_ThresholdTriggersLockout`: policy limit=2, window=5min, lockout=10min; 2 `invalid_credentials` rows within window → `loginTemporarilyLocked` returns true
  - `RF49_LockoutExpiresAfterLockoutMinutes`: same 2 failures but threshold crossing was >lockout_minutes ago → returns false
  - `RF49_NonCredentialFailuresDoNotExtendLockout`: `failed_login_limit_exceeded`, `device_revoked`, `max_devices_exceeded` rows are not counted (credentialFilter excludes them)
  - `RF49_UsersStatusNotMutated`: verify no `UPDATE auth.users SET status` in the login failure path
  - `RF49_UnknownEmailPathGeneric`: user not found → `dummyVerify` called → same `ErrInvalidCredentials` returned

- [ ] **Step 3: Verify brute-force tests**

  ```bash
  cd services/auth-service && go test -count=1 ./internal/service ./internal/storage -run 'RF49|Lockout|Login'
  ```

  Expected: pass.

---

### Task 8: auth-service — login attempts store and service (Scope C)

**Files:**

- Create: `services/auth-service/internal/storage/login_attempts_store.go`
- Create: `services/auth-service/internal/storage/login_attempts_store_test.go`
- Create: `services/auth-service/internal/service/login_attempts_service.go`
- Create: `services/auth-service/internal/service/login_attempts_service_test.go`
- Modify: `services/auth-service/internal/domain/auth.go`

- [ ] **Step 1: Add domain types**

  Add to `domain/auth.go`:

  ```go
  type LoginAttempt struct {
      ID            int64
      Email         string
      IPAddress     string   // raw; masking happens in HTTP layer
      UserAgent     string
      FailureReason string
      CreatedAt     time.Time
  }

  type LoginAttemptsCursor struct {
      CreatedAt time.Time
      ID        int64
  }
  ```

- [ ] **Step 2: Create store**

  `PGXLoginAttemptsStore.GetUserFailedAttempts(ctx, userID string, limit int, cursor *domain.LoginAttemptsCursor) ([]domain.LoginAttempt, error)`:

  ```sql
  SELECT id, email, success, failure_reason,
         ip_address::text, user_agent, created_at
  FROM auth.login_attempts
  WHERE user_id = $1
    AND success = false
    AND ($2::timestamptz IS NULL OR (created_at, id) < ($2, $3))
  ORDER BY created_at DESC, id DESC
  LIMIT $4
  ```

  Fetches `limit` rows (caller passes `limit+1` to detect next page).

- [ ] **Step 3: Write failing store tests**

  Use pgxmock: returns own failed rows only; cursor filters correctly; no `success=true` rows returned; query does not expose password/token columns.

- [ ] **Step 4: Run red store tests**

  ```bash
  cd services/auth-service && go test -count=1 ./internal/storage -run 'LoginAttempts'
  ```

  Expected: compile failure.

- [ ] **Step 5: Implement store**

- [ ] **Step 6: Create service**

  `LoginAttemptsService.GetMyAttempts(ctx, userID string, limit int, cursorStr string) ([]domain.LoginAttempt, nextCursor string, error)`:
  - Clamp `limit` to `[1, 100]`; default 50
  - Decode base64(JSON) cursor → `LoginAttemptsCursor`; invalid → `ErrInvalidInput`
  - Call store with `limit+1`; if `len(rows) == limit+1`, drop last, encode next cursor

- [ ] **Step 7: Write failing service tests**

  Cover: limit clamped; default 50 applied when ≤0; next cursor present when store returns limit+1 rows; no next cursor when fewer rows returned; invalid cursor string returns error.

- [ ] **Step 8: Run red service tests**

  ```bash
  cd services/auth-service && go test -count=1 ./internal/service -run 'LoginAttempts'
  ```

  Expected: compile failure.

- [ ] **Step 9: Implement service**

- [ ] **Step 10: Verify store + service tests**

  ```bash
  cd services/auth-service && go test -count=1 ./internal/storage ./internal/service -run 'LoginAttempts'
  ```

  Expected: pass.

---

### Task 9: auth-service — bearer middleware and HTTP handler (Scope C)

**Files:**

- Create: `services/auth-service/internal/http/bearer_middleware.go`
- Create: `services/auth-service/internal/http/bearer_middleware_test.go`
- Create: `services/auth-service/internal/http/login_attempts_handler.go`
- Create: `services/auth-service/internal/http/login_attempts_handler_test.go`
- Modify: `services/auth-service/internal/http/routes.go`
- Modify: `services/auth-service/internal/http/router.go`
- Modify: `services/auth-service/internal/app/app.go`

- [ ] **Step 1: Add route constant**

  ```go
  RouteAuthMeLoginAttempts = "/auth/me/login-attempts"
  ```

- [ ] **Step 2: Create bearer middleware**

  `BearerAuth(tokens *service.TokenManager) func(http.Handler) http.Handler`:
  - Extract `Authorization: Bearer <token>`; missing → 401 `{"error":"unauthorized"}`
  - `tokens.ValidateAccessToken(raw)`; invalid/expired → 401 same
  - Inject `userID` (= `claims.Subject`) via `context.WithValue(r.Context(), ctxKeyUserID, userID)`
  - No token detail in any error response

- [ ] **Step 3: Write failing bearer middleware tests**

  Cover: no header → 401; `Bearer ` with empty token → 401; expired JWT → 401; valid JWT → injects userID, calls next; `Authorization: Basic` → 401.

- [ ] **Step 4: Create login attempts handler**

  `GetMyLoginAttempts(svc LoginAttemptsManager, tokens *service.TokenManager)`:
  - Extract userID from context (set by bearer middleware)
  - Parse `?limit` (int, default 50) and `?cursor`
  - Call `svc.GetMyAttempts(ctx, userID, limit, cursorStr)`
  - IP masking: IPv4 → replace last two octets with `*.*`; IPv6 → replace all but first group with `*`; unparseable → omit field
  - `user_agent` truncated to 200 chars, non-printable chars stripped
  - Response: `{"data":[...], "pagination":{"limit":N,"next_cursor":null|"..."}}`

  `id` in response: `strconv.FormatInt(attempt.ID, 10)` (string to avoid JS int overflow).

- [ ] **Step 5: Write failing handler tests**

  Cover: no bearer → 401; invalid bearer → 401; valid bearer returns only own `success=false` rows; limit clamped at 100; `next_cursor` present when more rows exist; response contains no password/token/hash; IPv4 masked; `user_agent` truncated; `id` is string type in JSON.

- [ ] **Step 6: Wire handler into router**

  ```go
  mux.Handle(RouteAuthMeLoginAttempts, httputil.MethodNotAllowed(http.MethodGet,
      BearerAuth(tokens)(GetMyLoginAttempts(loginAttempts, tokens)),
  ))
  ```

  Handler receives `nil` service → returns 503 like other endpoints.

- [ ] **Step 7: Wire service in app.go**

  Instantiate `service.NewLoginAttemptsService(storage.NewPGXLoginAttemptsStore(pool))` when `pool != nil`. Pass to router.

- [ ] **Step 8: Verify HTTP tests**

  ```bash
  cd services/auth-service && go test -count=1 ./internal/http ./internal/app -run 'LoginAttempts|Bearer|Router|App'
  ```

  Expected: pass.

---

### Task 10: K8s / infra secrets and docs

**Files:**

- Modify: `infra/k8s/base/services/notification-service/deployment.yaml` (if exists)
- Modify: `infra/k8s/secrets/templates/nchat-secrets.template.yaml` (if exists)
- Create: `docs/runbooks/task-smtp-bruteforce-login-audit.md`
- Modify: `README.md`

- [ ] **Step 1: Update K8s templates (if files exist)**

  Add SMTP vars via `secretKeyRef` in notification-service deployment. Add placeholders in secrets template using `""` for `SMTP_PASSWORD` and `AUTH_EMAIL_OUTBOX_ENCRYPTION_KEY`. `SMTP_WORKER_ENABLED: "false"` in ConfigMap.

- [ ] **Step 2: Create runbook**

  `docs/runbooks/task-smtp-bruteforce-login-audit.md` covering:
  - RF-35/RF-49/RF-50/RNF-25 traceability (partial/foundation status)
  - Endpoints: `GET /auth/me/login-attempts`
  - SMTP config variables and `SMTP_WORKER_ENABLED` flag
  - Local SMTP smoke test with Mailpit (`docker run` command, note on `infra/compose/compose.dev.yml` not using profiles)
  - Sealed Secrets / `secretKeyRef` requirements
  - Security notes: no credentials in repo, TLS requirements, `none` only in dev/test/local
  - Out of scope: notification preference centre, digest batching, DND/URGENT, final RBAC, frontend UI, Valkey scheduler, general `notification_outbox`
  - Known limitation: at-least-once SMTP delivery (crash between send and finalise may re-send)

- [ ] **Step 3: Update README**

  Add brief notes to auth and notification sections: login attempts endpoint, SMTP worker opt-in, local dev Mailpit command.

- [ ] **Step 4: Verify docs formatting**

  ```bash
  pnpm format:check:docs
  ```

  Expected: pass.

---

### Task 11: GitHub issues and final validation

**Files:**

- All touched files

- [ ] **Step 1: Find or create GitHub issues**

  Search existing issues for "SMTP transacional", "brute-force protection", and "tentativas falhas". Link or create:
  - Issue: "Configurar SMTP transacional" (RF-35)
  - Issue: "Implementar brute-force protection" (RF-49)
  - Issue: "Implementar log de tentativas falhas" (RF-50)

- [ ] **Step 2: Run full validation suite**

  ```bash
  bash -n scripts/db/migrate.sh scripts/ci/migrations-check.sh
  pnpm migrations:check
  cd services/auth-service && go test -count=1 ./...
  cd services/notification-service && go test -count=1 ./...
  cd libs/go/platform && go test -count=1 ./emailcrypto/...
  pnpm fmt:go
  pnpm format:check:docs
  pnpm lint:go
  pnpm vet:go
  pnpm test:coverage:go:check
  pnpm run ci
  make ci
  semgrep scan --config p/owasp-top-ten --config p/secrets \
    services/auth-service services/notification-service \
    migrations/auth docs/runbooks/task-smtp-bruteforce-login-audit.md
  ```

  Expected: all pass; coverage ≥ 90%.

- [ ] **Step 3: Run code review and security review**

  Use the configured `code-review` and `security-code-review` skills. Key areas: SMTP credential handling, decrypted payload in-memory only, bearer token extraction and validation, login attempts cross-user isolation, IP masking, no token/password/hash in API responses or logs.

- [ ] **Step 4: Commit implementation**

  ```bash
  git add .
  git commit -m "feat(auth): add SMTP delivery brute force hardening and login audit

  Co-authored-by: Copilot <223556219+Copilot@users.noreply.github.com>"
  ```

- [ ] **Step 5: Push and open PR**

  Push `feat/auth-smtp-bruteforce-login-audit` and open PR to `develop`:
  - **Title:** `feat(auth): add SMTP delivery brute force hardening and login audit`
  - **Body must include:** issues closed, RF-35/RF-49/RF-50/RNF-25 traceability, endpoint `GET /auth/me/login-attempts`, SMTP config vars, security notes (no credentials in repo, TLS enforcement, in-memory token handling), out of scope, test plan with concrete cases, known limitation (at-least-once delivery)
  - Do **not** merge.
