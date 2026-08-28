# Auth Session Recovery Invites Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement RF-46, RF-48, and RF-51 in auth-service: session idle expiry enforcement, password recovery with expirable hashed tokens, and admin email invites with expirable hashed tokens.

**Architecture:** Keep all raw reset/invite tokens in memory only, persist only HMAC-SHA-256 hashes, and record email handoff through an encrypted `auth.email_outbox` token handoff. Services own normalization, policy validation, token hashing, and password hashing; stores own transactional persistence and row locks. HTTP handlers provide body caps, rate limits, generic enumeration-safe responses, and no frontend or SMTP behavior.

**Tech Stack:** Go 1.25, pgx/pgxmock, PostgreSQL migrations, existing auth-service domain/service/storage/http layers, pnpm/make validation, Semgrep security scan.

---

### Task 1: Migration, Policy Fields, and Route Constants

**Files:**

- Create: `migrations/auth/000004_auth_session_recovery_invites.up.sql`
- Create: `migrations/auth/000004_auth_session_recovery_invites.down.sql`
- Modify: `services/auth-service/internal/domain/user.go`
- Modify: `services/auth-service/internal/storage/user_store.go`
- Modify: `services/auth-service/internal/storage/login_store.go`
- Modify: `services/auth-service/internal/http/routes.go`

- [ ] **Step 1: Write migration files**

Create `000004` with `BEGIN`, `SET LOCAL search_path = auth, public`, add `password_reset_token_ttl_minutes`, `invite_token_ttl_hours`, and encrypted `auth.email_outbox` token handoff with `reset_token_id`, `invite_id`, nullable `user_id`, non-sensitive `payload`, status fields, and reference check.

- [ ] **Step 2: Extend policy struct and scans**

Add:

```go
PasswordResetTokenTTLMinutes int
InviteTokenTTLHours          int
```

to `domain.PolicySettings`; scan both fields in `PGXUserStore.GetPolicySettings` and `selectLoginPolicy`.

- [ ] **Step 3: Add route constants**

Add:

```go
RouteAuthPasswordForgot = "/auth/password/forgot"
RouteAuthPasswordReset  = "/auth/password/reset"
RouteAdminInvites       = "/admin/invites"
RouteAuthInvitesAccept  = "/auth/invites/accept"
```

- [ ] **Step 4: Verify migration syntax gate**

Run: `bash -n scripts/db/migrate.sh scripts/ci/migrations-check.sh`
Expected: exit 0.

Run: `pnpm migrations:check`
Expected: exit 0.

### Task 2: Session Expiry Enforcement

**Files:**

- Modify: `services/auth-service/internal/service/session_service_test.go`
- Modify: `services/auth-service/internal/service/auth_service.go`
- Modify: `services/auth-service/internal/storage/session_store_test.go`
- Modify: `services/auth-service/internal/storage/session_store.go`
- Modify: `services/auth-service/internal/http/auth_handler_test.go`

- [ ] **Step 1: Write failing service test for no absolute expiry extension**

Update fake store signature to `RotateRefreshToken(ctx, oldHash, newHash string)` and assert `AuthService.Refresh` no longer passes refresh-token expiry into the store.

- [ ] **Step 2: Run red test**

Run: `cd services/auth-service && go test -count=1 ./internal/service -run TestAuthService_RefreshRotatesToken`
Expected: compile failure because interface still takes `expiresAt`.

- [ ] **Step 3: Implement service interface change**

Remove `time.Time` from `SessionStore.RotateRefreshToken`; in `AuthService.Refresh`, ignore the refresh token absolute expiry returned by `GenerateRefreshToken` and call `store.RotateRefreshToken(ctx, oldHash, newRefreshHash)`.

- [ ] **Step 4: Write failing store test for policy idle extension**

Update `TestPGXSessionStore_RotateRefreshToken_SuccessRecordsHistory` to expect a policy query for `session_idle_timeout_minutes` and an `UPDATE auth.user_sessions` with a `time.Time` near `now()+policyTTL`, not refresh TTL.

- [ ] **Step 5: Run red store test**

Run: `cd services/auth-service && go test -count=1 ./internal/storage -run TestPGXSessionStore_RotateRefreshToken_SuccessRecordsHistory`
Expected: fail because store still expects `expiresAt` argument and does not query policy.

- [ ] **Step 6: Implement store rotation fix**

Inside the rotation transaction, after selecting/locking the session, select `session_idle_timeout_minutes` from `auth.auth_policy_settings`, compute `idleExpiresAt := time.Now().UTC().Add(time.Duration(policy.SessionIdleTimeoutMinutes) * time.Minute)`, update only `idle_expires_at` and `last_seen_at`, and never update `absolute_expires_at`.

- [ ] **Step 7: Verify session tests**

Run: `cd services/auth-service && go test -count=1 ./internal/service ./internal/storage ./internal/http -run 'Refresh|Logout'`
Expected: pass.

### Task 3: Domain Errors, Inputs, and Token Helpers

**Files:**

- Modify: `services/auth-service/internal/domain/auth.go`
- Modify: `services/auth-service/internal/domain/errors.go`
- Modify: `services/auth-service/internal/service/token_service.go`
- Modify: `services/auth-service/internal/service/token_service_test.go`

- [ ] **Step 1: Write failing token helper tests**

Add tests that `GenerateOpaqueToken` returns non-empty base64url token, password reset and invite hashes differ for the same raw token, hashes do not equal or contain raw token, and hashing is deterministic.

- [ ] **Step 2: Run red token tests**

Run: `cd services/auth-service && go test -count=1 ./internal/service -run 'Opaque|PasswordResetToken|InviteToken'`
Expected: compile failure for missing helpers.

- [ ] **Step 3: Add domain types and errors**

Add `ForgotPasswordInput`, `ResetPasswordInput`, `AdminInviteInput`, `InviteResult`, `AcceptInviteInput`, `AcceptInviteResult`, `ErrInvalidToken`, and `ErrInviteAlreadyPending`.

- [ ] **Step 4: Implement token helpers**

Use existing `randomOpaqueString(32)` for `GenerateOpaqueToken`; add domain-separated HMAC helpers with prefixes `nchat-password-reset-v1:` and `nchat-invite-v1:`.

- [ ] **Step 5: Verify token tests**

Run: `cd services/auth-service && go test -count=1 ./internal/domain ./internal/service -run 'Opaque|PasswordResetToken|InviteToken|Validate'`
Expected: pass.

### Task 4: Password Reset Service and Store

**Files:**

- Create: `services/auth-service/internal/service/password_reset_service.go`
- Create: `services/auth-service/internal/service/password_reset_service_test.go`
- Create: `services/auth-service/internal/storage/password_reset_store.go`
- Create: `services/auth-service/internal/storage/password_reset_store_test.go`

- [ ] **Step 1: Write failing service tests**

Cover known active user creating hashed token and outbox request, unknown user generic success with no token, invalid email generic success, weak password rejected, and valid reset hashing password before store call.

- [ ] **Step 2: Run red service tests**

Run: `cd services/auth-service && go test -count=1 ./internal/service -run 'PasswordReset|ForgotPassword|ResetPassword'`
Expected: compile failure for missing service.

- [ ] **Step 3: Implement password reset service**

Normalize email; generic-return on invalid/unknown/ineligible; generate opaque raw token, hash with `HashPasswordResetToken`, store only hash; validate reset token/password; hash new password with `HashPassword`; call transaction store.

- [ ] **Step 4: Write failing store tests**

Use pgxmock to assert active-user lookup filters `status='active'`, `deleted_at IS NULL`, `auth_source='manual'`; token creation supersedes previous unused tokens and inserts `email_outbox` with `reset_token_id` and no token/link/hash payload; reset locks token, rejects expired/used/unknown, updates credential, marks used, revokes sessions and active token history.

- [ ] **Step 5: Run red store tests**

Run: `cd services/auth-service && go test -count=1 ./internal/storage -run 'PasswordReset'`
Expected: compile failure for missing store.

- [ ] **Step 6: Implement password reset store**

Create `PGXPasswordResetStore` with `GetActiveUserForPasswordReset`, `GetPolicySettings`, `CreatePasswordResetToken`, and `ResetPasswordTx`. Keep SQL parameterized and scoped to `auth.*`.

- [ ] **Step 7: Verify password reset layer tests**

Run: `cd services/auth-service && go test -count=1 ./internal/service ./internal/storage -run 'PasswordReset|ForgotPassword|ResetPassword'`
Expected: pass.

### Task 5: Password Reset HTTP and Wiring

**Files:**

- Create: `services/auth-service/internal/http/password_handler.go`
- Create: `services/auth-service/internal/http/password_handler_test.go`
- Modify: `services/auth-service/internal/http/router.go`
- Modify: `services/auth-service/internal/app/app.go`

- [ ] **Step 1: Write failing handler tests**

Cover forgot always 202 for known/unknown service outcomes, reset 204 success, invalid token 401 generic, weak password 400, oversized body 413, rate limit 429 through router, responses not containing token/password/hash strings.

- [ ] **Step 2: Run red handler tests**

Run: `cd services/auth-service && go test -count=1 ./internal/http -run 'Password'`
Expected: compile failure for missing routes/handlers.

- [ ] **Step 3: Implement handlers and router wiring**

Add `PasswordRecoveryManager` interface, request structs, 4 KiB body cap reuse, `AuthForgotPassword`, `AuthResetPassword`, and route wiring with a recovery-specific token limiter. Instantiate `PasswordResetService` in `app.New` when DB and token manager are configured.

- [ ] **Step 4: Verify HTTP tests**

Run: `cd services/auth-service && go test -count=1 ./internal/http ./internal/app -run 'Password|Router|App'`
Expected: pass.

### Task 6: Invite Service and Store

**Files:**

- Create: `services/auth-service/internal/service/invite_service.go`
- Create: `services/auth-service/internal/service/invite_service_test.go`
- Create: `services/auth-service/internal/storage/invite_store.go`
- Create: `services/auth-service/internal/storage/invite_store_test.go`

- [ ] **Step 1: Write failing invite service tests**

Cover create invite normalizes email, rejects invalid display name/email, duplicate user, active pending invite, creates hashed token, never exposes raw token, weak accept password rejected, accept hashes token and password before store call.

- [ ] **Step 2: Run red service tests**

Run: `cd services/auth-service && go test -count=1 ./internal/service -run 'Invite'`
Expected: compile failure for missing invite service.

- [ ] **Step 3: Implement invite service**

Normalize and validate email/display name; detect duplicate user/pending invite; generate raw token in memory; hash with `HashInviteToken`; use policy TTL; return safe `InviteResult`; accept invite validates password and calls store with token hash/password hash.

- [ ] **Step 4: Write failing invite store tests**

Use pgxmock to cover duplicate user lookup, active pending invite query, invite insert returning id/created_at, encrypted outbox insert with `invite_id`, valid accept creates user and credential then marks invite accepted, expired/used/revoked/unknown token returns `ErrInvalidToken`.

- [ ] **Step 5: Run red store tests**

Run: `cd services/auth-service && go test -count=1 ./internal/storage -run 'Invite'`
Expected: compile failure for missing invite store.

- [ ] **Step 6: Implement invite store**

Create `PGXInviteStore` with transactional create/accept, `FOR UPDATE` locks on token rows, unique-email handling mapped to `ErrDuplicateEmail`, and encrypted outbox inserts.

- [ ] **Step 7: Verify invite layer tests**

Run: `cd services/auth-service && go test -count=1 ./internal/service ./internal/storage -run 'Invite'`
Expected: pass.

### Task 7: Invite HTTP and Wiring

**Files:**

- Create: `services/auth-service/internal/http/invite_handler.go`
- Create: `services/auth-service/internal/http/invite_handler_test.go`
- Modify: `services/auth-service/internal/http/router.go`
- Modify: `services/auth-service/internal/app/app.go`

- [ ] **Step 1: Write failing invite handler tests**

Cover missing admin token 503, wrong token 401, duplicate existing user 409, active pending invite 409, create response has no token/hash, accept valid invite 201 safe summary, invalid token generic 401, weak password 400, oversized accept body 413, rate limit 429 for public accept.

- [ ] **Step 2: Run red handler tests**

Run: `cd services/auth-service && go test -count=1 ./internal/http -run 'Invite|AdminInvites'`
Expected: compile failure for missing handlers/routes.

- [ ] **Step 3: Implement invite handlers and router wiring**

Add `InviteManager` interface, `AdminCreateInvite`, `AuthAcceptInvite`, guarded `/admin/invites`, rate-limited `/auth/invites/accept`, 4 KiB body caps, generic token responses, and safe JSON output.

- [ ] **Step 4: Verify invite HTTP tests**

Run: `cd services/auth-service && go test -count=1 ./internal/http ./internal/app -run 'Invite|AdminInvites|Router|App'`
Expected: pass.

### Task 8: Documentation and Issue/PR Metadata

**Files:**

- Create: `docs/runbooks/task-auth-session-recovery-invites.md`
- Modify: `README.md`

- [ ] **Step 1: Update docs**

Document RF-46/RF-48/RF-51 traceability, endpoints, encrypted outbox token handoff, key requirements, security decisions, known limitations, and out-of-scope frontend/SMTP/OAuth/RBAC/notification worker/auto-login.

- [ ] **Step 2: Link issues in planned PR body**

Use existing issues: closes #55, closes #56, closes #57.

- [ ] **Step 3: Verify docs formatting**

Run: `pnpm format:check:docs`
Expected: pass.

### Task 9: Final Verification, Review, Commit, and PR

**Files:**

- All touched files

- [ ] **Step 1: Run required validation**

Run, in order:

```bash
bash -n scripts/db/migrate.sh scripts/ci/migrations-check.sh
pnpm migrations:check
cd services/auth-service && go test -count=1 ./...
pnpm fmt:go
pnpm format:check:docs
pnpm lint:go
pnpm vet:go
pnpm test:coverage:go:check
pnpm run ci
make ci
semgrep scan --config p/owasp-top-ten --config p/secrets services/auth-service migrations/auth docs/runbooks/task-auth-session-recovery-invites.md
```

Expected: all pass; coverage remains >= 90%.

- [ ] **Step 2: Run code review and security review**

Use the configured code-review and security-code-review skills. Validate auth/public endpoints, token storage, outbox contents, rate limits, body caps, and logging/response exposure.

- [ ] **Step 3: Commit implementation**

Run:

```bash
git add .
git commit -m "feat(auth): add session expiry recovery and invites"
```

- [ ] **Step 4: Push and open PR**

Push `feat/auth-session-recovery-invites` and open PR to `develop` with title `feat(auth): add session expiry recovery and invites`. PR body must include issues closed (#55/#56/#57), RF traceability, endpoints, security notes, out of scope, test plan, and known limitations. Do not merge the PR.
