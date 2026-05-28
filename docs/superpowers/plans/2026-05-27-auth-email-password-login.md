# Email Password Login Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add `POST /auth/login` for manual e-mail/password users with temporary login-attempt lockout, transactional session creation, optional device handling, and JWT/refresh-token issuance.

**Architecture:** The HTTP layer decodes and rate-limits `/auth/login`, then calls a `LoginManager` service. The service normalizes/validates input, uses the existing `TokenManager` for refresh-token generation and access-token signing, and delegates all DB state changes to a transactional `PGXLoginStore`. The store verifies credentials, records attempts, enforces temporary lockout, manages optional devices, inserts `user_sessions` plus initial `refresh_token_history`, and updates `last_login_at`.

**Tech Stack:** Go 1.25, net/http, pgx/pgxmock, Argon2id, HMAC-SHA-256 token hashing from existing `TokenManager`, PostgreSQL migrations, pnpm/make CI scripts.

---

## File Structure

- Create `migrations/auth/000003_auth_login_policy_window.up.sql`: add failed-login window and lockout policy columns.
- Create `migrations/auth/000003_auth_login_policy_window.down.sql`: remove the new policy columns and constraints.
- Modify `services/auth-service/internal/domain/errors.go`: add `ErrInvalidCredentials`.
- Modify `services/auth-service/internal/domain/auth.go`: add safe login user/result/session context structs.
- Modify `services/auth-service/internal/domain/user.go`: extend `PolicySettings`.
- Modify `services/auth-service/internal/service/password.go`: add Argon2id PHC verification and dummy verification helper.
- Modify `services/auth-service/internal/service/password_test.go`: cover verify success/failure/malformed/dummy behavior.
- Create `services/auth-service/internal/service/login_service.go`: implement `LoginService`.
- Create `services/auth-service/internal/service/login_service_test.go`: cover normalization, token reuse, safe response, raw secret handling boundaries.
- Create `services/auth-service/internal/storage/login_store.go`: implement transactional login persistence.
- Create `services/auth-service/internal/storage/login_store_test.go`: pgxmock tests for success, failures, lockout, devices, sessions, token history.
- Modify `services/auth-service/internal/storage/user_store.go`: select new policy fields for existing user-creation policy reads.
- Modify `services/auth-service/internal/storage/user_store_test.go`: expect and assert new policy fields.
- Modify `services/auth-service/internal/http/routes.go`: add `RouteAuthLogin`.
- Modify `services/auth-service/internal/http/auth_handler.go`: add login handler, request/response types, generic invalid credentials mapping.
- Modify `services/auth-service/internal/http/auth_handler_test.go`: cover login HTTP success/error/body-cap safety.
- Modify `services/auth-service/internal/http/router.go`: wire `/auth/login` with the same limiter/body cap path.
- Modify `services/auth-service/internal/http/router_test.go`: cover method and rate-limit behavior.
- Modify `services/auth-service/internal/app/app.go`: construct `LoginService` only when DB and token manager are available.
- Modify `docs/runbooks/task-email-password-login.md`: document endpoint, lockout, limits, and out-of-scope items.
- Modify `README.md`: add `/auth/login` and link the runbook.

## Task 1: Migration And Policy Shape

**Files:**

- Create: `migrations/auth/000003_auth_login_policy_window.up.sql`
- Create: `migrations/auth/000003_auth_login_policy_window.down.sql`
- Modify: `services/auth-service/internal/domain/user.go`
- Modify: `services/auth-service/internal/storage/user_store.go`
- Modify: `services/auth-service/internal/storage/user_store_test.go`

- [ ] **Step 1.1: Write migration files**

Create `migrations/auth/000003_auth_login_policy_window.up.sql`:

```sql
-- 000003_auth_login_policy_window.up.sql
-- Adds policy fields required for temporary login-attempt lockout.

BEGIN;

ALTER TABLE auth.auth_policy_settings
  ADD COLUMN failed_login_window_minutes INT NOT NULL DEFAULT 15,
  ADD COLUMN failed_login_lockout_minutes INT NOT NULL DEFAULT 15;

ALTER TABLE auth.auth_policy_settings
  ADD CONSTRAINT auth_policy_settings_failed_login_window_check
    CHECK (failed_login_window_minutes > 0),
  ADD CONSTRAINT auth_policy_settings_failed_login_lockout_check
    CHECK (failed_login_lockout_minutes > 0);

COMMIT;
```

Create `migrations/auth/000003_auth_login_policy_window.down.sql`:

```sql
-- 000003_auth_login_policy_window.down.sql
-- Removes temporary login-attempt lockout policy fields.

BEGIN;

ALTER TABLE auth.auth_policy_settings
  DROP CONSTRAINT IF EXISTS auth_policy_settings_failed_login_lockout_check,
  DROP CONSTRAINT IF EXISTS auth_policy_settings_failed_login_window_check,
  DROP COLUMN IF EXISTS failed_login_lockout_minutes,
  DROP COLUMN IF EXISTS failed_login_window_minutes;

COMMIT;
```

- [ ] **Step 1.2: Run migration validation and verify it passes**

Run:

```bash
pnpm migrations:check
```

Expected: pass with no plaintext credential-column or transaction-wrapper failures.

- [ ] **Step 1.3: Extend policy domain fields**

Modify `services/auth-service/internal/domain/user.go` so `PolicySettings` becomes:

```go
type PolicySettings struct {
	MinPasswordLength            int
	RequireUppercase             bool
	RequireLowercase             bool
	RequireNumber                bool
	RequireSymbol                bool
	FailedLoginLimit             int
	FailedLoginWindowMinutes     int
	FailedLoginLockoutMinutes    int
	SessionIdleTimeoutMinutes    int
	MaxDevicesPerUser            int
}
```

- [ ] **Step 1.4: Update existing policy read tests first**

In `services/auth-service/internal/storage/user_store_test.go`, change `TestPGXUserStore_GetPolicySettings_Success` rows to include:

```go
mock.ExpectQuery(`SELECT min_password_length`).
	WillReturnRows(pgxmock.NewRows([]string{
		"min_password_length", "require_uppercase", "require_lowercase",
		"require_number", "require_symbol", "failed_login_limit",
		"failed_login_window_minutes", "failed_login_lockout_minutes",
		"session_idle_timeout_minutes", "max_devices_per_user",
	}).AddRow(12, true, true, true, true, 5, 15, 15, 60, 5))
```

Add assertions:

```go
if policy.FailedLoginLimit != 5 || policy.FailedLoginWindowMinutes != 15 || policy.FailedLoginLockoutMinutes != 15 {
	t.Fatalf("unexpected failed login policy: %+v", policy)
}
if policy.SessionIdleTimeoutMinutes != 60 || policy.MaxDevicesPerUser != 5 {
	t.Fatalf("unexpected session/device policy: %+v", policy)
}
```

- [ ] **Step 1.5: Run the focused test and verify it fails**

Run:

```bash
cd services/auth-service && go test ./internal/storage -run TestPGXUserStore_GetPolicySettings_Success -count=1
```

Expected: FAIL because `GetPolicySettings` still scans only the original five columns.

- [ ] **Step 1.6: Update `GetPolicySettings` query and scan**

Modify `services/auth-service/internal/storage/user_store.go`:

```go
err := s.pool.QueryRow(ctx, `
	SELECT min_password_length, require_uppercase, require_lowercase,
	       require_number, require_symbol, failed_login_limit,
	       failed_login_window_minutes, failed_login_lockout_minutes,
	       session_idle_timeout_minutes, max_devices_per_user
	FROM auth.auth_policy_settings
	WHERE id = 1`).Scan(
	&p.MinPasswordLength, &p.RequireUppercase, &p.RequireLowercase,
	&p.RequireNumber, &p.RequireSymbol, &p.FailedLoginLimit,
	&p.FailedLoginWindowMinutes, &p.FailedLoginLockoutMinutes,
	&p.SessionIdleTimeoutMinutes, &p.MaxDevicesPerUser,
)
```

- [ ] **Step 1.7: Run focused storage tests and verify they pass**

Run:

```bash
cd services/auth-service && go test ./internal/storage -run TestPGXUserStore_GetPolicySettings -count=1
```

Expected: PASS.

## Task 2: Password Verification And Login Domain Types

**Files:**

- Modify: `services/auth-service/internal/domain/errors.go`
- Modify: `services/auth-service/internal/domain/auth.go`
- Modify: `services/auth-service/internal/service/password.go`
- Modify: `services/auth-service/internal/service/password_test.go`

- [ ] **Step 2.1: Add failing password verification tests**

Append to `services/auth-service/internal/service/password_test.go`:

```go
func TestVerifyPassword_AcceptsMatchingArgon2idPHCHash(t *testing.T) {
	hash, err := service.HashPassword("ChangeMe@123")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	ok, err := service.VerifyPassword("ChangeMe@123", hash)
	if err != nil {
		t.Fatalf("VerifyPassword: %v", err)
	}
	if !ok {
		t.Fatal("expected password to verify")
	}
}

func TestVerifyPassword_RejectsWrongPassword(t *testing.T) {
	hash, err := service.HashPassword("ChangeMe@123")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	ok, err := service.VerifyPassword("wrong-password", hash)
	if err != nil {
		t.Fatalf("VerifyPassword: %v", err)
	}
	if ok {
		t.Fatal("expected wrong password to fail")
	}
}

func TestVerifyPassword_RejectsMalformedHash(t *testing.T) {
	ok, err := service.VerifyPassword("ChangeMe@123", "not-a-phc-hash")
	if err == nil {
		t.Fatal("expected malformed hash error")
	}
	if ok {
		t.Fatal("malformed hash must not verify")
	}
}

func TestRunDummyPasswordVerification_DoesNotReturnPasswordMaterial(t *testing.T) {
	service.RunDummyPasswordVerification("unknown-user-password")
}
```

- [ ] **Step 2.2: Run focused tests and verify they fail**

Run:

```bash
cd services/auth-service && go test ./internal/service -run 'TestVerifyPassword|TestRunDummyPasswordVerification' -count=1
```

Expected: FAIL because `VerifyPassword` and `RunDummyPasswordVerification` do not exist.

- [ ] **Step 2.3: Add invalid credentials and login domain types**

Modify `services/auth-service/internal/domain/errors.go`:

```go
var ErrInvalidCredentials = errors.New("invalid credentials")
```

Append to `services/auth-service/internal/domain/auth.go`:

```go
type LoginInput struct {
	Email             string
	Password          string
	DeviceFingerprint string
	DeviceName        string
	Platform          string
	IPAddress         string
	UserAgent         string
}

type LoginUser struct {
	ID                 string
	Email              string
	DisplayName        string
	MustChangePassword bool
}

type LoginResult struct {
	AccessToken  string
	RefreshToken string
	TokenType    string
	ExpiresIn    int
	User         LoginUser
}

type CreateSessionInput struct {
	UserID           string
	Email            string
	RefreshTokenHash string
	RefreshExpiresAt time.Time
	DeviceFingerprintHash string
	DeviceName      string
	Platform        string
	IPAddress       string
	UserAgent        string
}

type CreatedLoginSession struct {
	Session Session
	User    LoginUser
}
```

Add `import "time"` to `auth.go`.

- [ ] **Step 2.4: Implement PHC parser and verifier**

In `services/auth-service/internal/service/password.go`, add imports:

```go
import (
	"crypto/subtle"
	"strconv"
	"strings"
)
```

Add:

```go
const dummyPasswordHash = "$argon2id$v=19$m=65536,t=3,p=4$c3RhdGljLWR1bW15LXNhbHQ$Vzkb2CNBv8Y7vIgM2XgaXW7J5z/JpUnL23fknD1E8VI"

func VerifyPassword(password string, encodedHash string) (bool, error) {
	params, salt, expected, err := parseArgon2idPHC(encodedHash)
	if err != nil {
		return false, err
	}
	actual := argon2.IDKey([]byte(password), salt, params.iterations, params.memory, params.parallelism, uint32(len(expected)))
	return subtle.ConstantTimeCompare(actual, expected) == 1, nil
}

func RunDummyPasswordVerification(password string) {
	_, _ = VerifyPassword(password, dummyPasswordHash)
}

type argon2idParams struct {
	memory      uint32
	iterations uint32
	parallelism uint8
}
```

Implement `parseArgon2idPHC` using `strings.Split(hash, "$")`, `base64.RawStdEncoding.DecodeString`, `strconv.ParseUint`, and strict checks for algorithm `argon2id` and version `v=19`.

- [ ] **Step 2.5: Run focused service tests and verify they pass**

Run:

```bash
cd services/auth-service && go test ./internal/service -run 'TestVerifyPassword|TestRunDummyPasswordVerification' -count=1
```

Expected: PASS.

## Task 3: Login Service

**Files:**

- Create: `services/auth-service/internal/service/login_service.go`
- Create: `services/auth-service/internal/service/login_service_test.go`

- [ ] **Step 3.1: Write failing login service tests**

Create `services/auth-service/internal/service/login_service_test.go` with fake store tests:

```go
package service_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nicrepository/nchat/services/auth-service/internal/domain"
	"github.com/nicrepository/nchat/services/auth-service/internal/service"
)

type fakeLoginStore struct {
	input domain.CreateSessionInput
	result domain.CreatedLoginSession
	err error
}

func (f *fakeLoginStore) CreateLoginSession(_ context.Context, input domain.CreateSessionInput) (domain.CreatedLoginSession, error) {
	f.input = input
	return f.result, f.err
}

func TestLoginService_LoginCreatesSessionWithHashedRefreshAndDeviceFingerprint(t *testing.T) {
	manager := newTestTokenManager(t, strings.Repeat("l", 32))
	store := &fakeLoginStore{result: domain.CreatedLoginSession{
		Session: domain.Session{ID: "session-1", UserID: "user-1"},
		User: domain.LoginUser{ID: "user-1", Email: "user@example.com", DisplayName: "User", MustChangePassword: true},
	}}
	login := service.NewLoginService(manager, store)

	result, err := login.Login(context.Background(), domain.LoginInput{
		Email: "  USER@EXAMPLE.COM  ", Password: "ChangeMe@123",
		DeviceFingerprint: "raw-device", DeviceName: "Laptop", Platform: "linux",
		IPAddress: "203.0.113.10", UserAgent: "test-agent",
	})
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if store.input.Email != "user@example.com" {
		t.Fatalf("expected normalized email, got %q", store.input.Email)
	}
	if store.input.RefreshTokenHash == "" || store.input.RefreshTokenHash == result.RefreshToken {
		t.Fatal("refresh token must be hashed before storage")
	}
	if store.input.DeviceFingerprintHash == "" || store.input.DeviceFingerprintHash == "raw-device" {
		t.Fatal("device fingerprint must be hashed before storage")
	}
	if result.User.Email != "user@example.com" || !result.User.MustChangePassword {
		t.Fatalf("unexpected safe user: %+v", result.User)
	}
	if result.AccessToken == "" || result.RefreshToken == "" || result.TokenType != "Bearer" || result.ExpiresIn != 900 {
		t.Fatalf("unexpected token result: %+v", result)
	}
}

func TestLoginService_LoginRequiresEmailAndPassword(t *testing.T) {
	manager := newTestTokenManager(t, strings.Repeat("m", 32))
	login := service.NewLoginService(manager, &fakeLoginStore{})
	if _, err := login.Login(context.Background(), domain.LoginInput{Password: "x"}); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("expected invalid input for missing email, got %v", err)
	}
	if _, err := login.Login(context.Background(), domain.LoginInput{Email: "user@example.com"}); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("expected invalid input for missing password, got %v", err)
	}
}

func TestLoginService_LoginPropagatesInvalidCredentials(t *testing.T) {
	manager := newTestTokenManager(t, strings.Repeat("o", 32))
	login := service.NewLoginService(manager, &fakeLoginStore{err: domain.ErrInvalidCredentials})
	_, err := login.Login(context.Background(), domain.LoginInput{Email: "user@example.com", Password: "ChangeMe@123"})
	if !errors.Is(err, domain.ErrInvalidCredentials) {
		t.Fatalf("expected invalid credentials, got %v", err)
	}
}

var _ = time.Time{}
```

- [ ] **Step 3.2: Run focused tests and verify they fail**

Run:

```bash
cd services/auth-service && go test ./internal/service -run TestLoginService -count=1
```

Expected: FAIL because `NewLoginService` and `CreateLoginSession` interface do not exist.

- [ ] **Step 3.3: Implement `LoginService`**

Create `services/auth-service/internal/service/login_service.go`:

```go
package service

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/nicrepository/nchat/services/auth-service/internal/domain"
)

type LoginStore interface {
	CreateLoginSession(ctx context.Context, input domain.CreateSessionInput) (domain.CreatedLoginSession, error)
}

type LoginManager interface {
	Login(ctx context.Context, input domain.LoginInput) (domain.LoginResult, error)
}

type LoginService struct {
	tokens *TokenManager
	store LoginStore
}

func NewLoginService(tokens *TokenManager, store LoginStore) *LoginService {
	return &LoginService{tokens: tokens, store: store}
}

func (s *LoginService) Login(ctx context.Context, input domain.LoginInput) (domain.LoginResult, error) {
	input.Email = domain.NormalizeEmail(input.Email)
	input.DeviceFingerprint = strings.TrimSpace(input.DeviceFingerprint)
	input.DeviceName = strings.TrimSpace(input.DeviceName)
	input.Platform = strings.TrimSpace(input.Platform)
	if err := domain.ValidateEmail(input.Email); err != nil {
		return domain.LoginResult{}, err
	}
	if input.Password == "" {
		return domain.LoginResult{}, fmt.Errorf("%w: password is required", domain.ErrInvalidInput)
	}
	refreshToken, refreshHash, refreshExpiresAt, err := s.tokens.GenerateRefreshToken()
	if err != nil {
		return domain.LoginResult{}, err
	}
	sessionInput := domain.CreateSessionInput{
		Email: input.Email, RefreshTokenHash: refreshHash, RefreshExpiresAt: refreshExpiresAt,
		DeviceFingerprintHash: hashOptionalDeviceFingerprint(input.DeviceFingerprint),
		DeviceName: input.DeviceName, Platform: input.Platform, IPAddress: input.IPAddress,
		UserAgent: input.UserAgent,
	}
	created, err := s.store.CreateLoginSession(ctx, sessionInput)
	if err != nil {
		return domain.LoginResult{}, err
	}
	accessToken, expiresIn, err := s.tokens.GenerateAccessToken(created.Session.UserID, created.Session.ID)
	if err != nil {
		return domain.LoginResult{}, err
	}
	return domain.LoginResult{AccessToken: accessToken, RefreshToken: refreshToken, TokenType: bearerTokenType, ExpiresIn: expiresIn, User: created.User}, nil
}

func hashOptionalDeviceFingerprint(raw string) string {
	if raw == "" {
		return ""
	}
	sum := hmac.New(sha256.New, []byte("nchat-device-fingerprint-v1"))
	_, _ = sum.Write([]byte(raw))
	return hex.EncodeToString(sum.Sum(nil))
}
```

- [ ] **Step 3.4: Run focused service tests and verify they pass**

Run:

```bash
cd services/auth-service && go test ./internal/service -run TestLoginService -count=1
```

Expected: PASS.

## Task 4: Transactional Login Store

**Files:**

- Create: `services/auth-service/internal/storage/login_store.go`
- Create: `services/auth-service/internal/storage/login_store_test.go`

- [ ] **Step 4.1: Write failing pgxmock tests for success**

Create `services/auth-service/internal/storage/login_store_test.go` with a success test that expects:

```go
mock.ExpectBegin()
mock.ExpectQuery(`SELECT min_password_length`).
	WillReturnRows(pgxmock.NewRows([]string{
		"min_password_length", "require_uppercase", "require_lowercase",
		"require_number", "require_symbol", "failed_login_limit",
		"failed_login_window_minutes", "failed_login_lockout_minutes",
		"session_idle_timeout_minutes", "max_devices_per_user",
	}).AddRow(12, true, true, true, true, 5, 15, 15, 60, 5))
mock.ExpectQuery(`SELECT u\.id, u\.email::text, u\.display_name`).
	WithArgs("user@example.com").
	WillReturnRows(pgxmock.NewRows([]string{
		"id", "email", "display_name", "status", "deleted_at", "password_hash", "must_change_password",
	}).AddRow("user-1", "user@example.com", "User", "active", nil, passwordHash, true))
mock.ExpectQuery(`SELECT created_at FROM auth\.login_attempts`).
	WithArgs("user-1", 15).
	WillReturnRows(pgxmock.NewRows([]string{"created_at"}))
mock.ExpectExec(`INSERT INTO auth\.login_attempts`).
	WithArgs("user-1", "user@example.com", true, nil, "203.0.113.10", "agent").
	WillReturnResult(pgxmock.NewResult("INSERT", 1))
mock.ExpectQuery(`INSERT INTO auth\.user_sessions`).
	WithArgs("user-1", nil, "refresh-hash", "203.0.113.10", "agent", pgxmock.AnyArg(), refreshExpiresAt).
	WillReturnRows(pgxmock.NewRows([]string{"id", "user_id"}).AddRow("session-1", "user-1"))
mock.ExpectExec(`INSERT INTO auth\.refresh_token_history`).
	WithArgs("session-1", "refresh-hash").
	WillReturnResult(pgxmock.NewResult("INSERT", 1))
mock.ExpectExec(`UPDATE auth\.users`).
	WithArgs("user-1").
	WillReturnResult(pgxmock.NewResult("UPDATE", 1))
mock.ExpectCommit()
mock.ExpectRollback()
```

- [ ] **Step 4.2: Add failing storage tests for required failure paths**

Add tests that expect:

- wrong password inserts `success=false`, `failure_reason='invalid_credentials'`, returns `domain.ErrInvalidCredentials`
- unknown e-mail runs failure count by e-mail, inserts an attempt with `user_id = nil`, returns `domain.ErrInvalidCredentials`
- suspended/deleted/locked user inserts a generic failed attempt and returns `domain.ErrInvalidCredentials`
- pre-existing failures at threshold insert `failed_login_limit_exceeded`, create no session, and do not update `auth.users.status`
- no device fingerprint creates session with nil `device_id` and never inserts `auth.user_devices`
- existing device fingerprint updates device and uses returned `device_id`
- max devices exceeded records failed attempt and returns generic invalid credentials

- [ ] **Step 4.3: Run focused storage tests and verify they fail**

Run:

```bash
cd services/auth-service && go test ./internal/storage -run TestPGXLoginStore -count=1
```

Expected: FAIL because `PGXLoginStore` does not exist.

- [ ] **Step 4.4: Implement `PGXLoginStore` transaction**

Create `services/auth-service/internal/storage/login_store.go` with:

- `NewPGXLoginStore(pool Pool) *PGXLoginStore`
- `CreateLoginSession(ctx context.Context, input domain.CreateSessionInput) (domain.CreatedLoginSession, error)`
- helpers:
  - `selectLoginPolicy`
  - `selectLoginUser`
  - `recentFailureTimesByUser`
  - `recentFailureTimesByEmail`
  - `isTemporarilyLocked`
  - `recordLoginAttempt`
  - `resolveLoginDevice`
  - `insertLoginSession`
  - `insertInitialRefreshTokenHistory`

The main transaction flow:

```go
tx, err := s.pool.Begin(ctx)
if err != nil { return domain.CreatedLoginSession{}, fmt.Errorf("begin tx: %w", err) }
defer tx.Rollback(ctx) //nolint:errcheck

policy, err := selectLoginPolicy(ctx, tx)
if err != nil { return domain.CreatedLoginSession{}, err }

candidate, found, err := selectLoginUser(ctx, tx, input.Email)
if err != nil { return domain.CreatedLoginSession{}, err }

locked, err := loginTemporarilyLocked(ctx, tx, found, candidate.User.ID, input.Email, policy)
if err != nil { return domain.CreatedLoginSession{}, err }
if locked {
	_ = recordLoginAttempt(ctx, tx, nullableUserID(found, candidate.User.ID), input.Email, false, "failed_login_limit_exceeded", input.IPAddress, input.UserAgent)
	return domain.CreatedLoginSession{}, domain.ErrInvalidCredentials
}

if !found || candidate.User.Status != "active" || candidate.Deleted || candidate.PasswordHash == "" {
	if !found { service.RunDummyPasswordVerification("") }
	_ = recordLoginAttempt(ctx, tx, nullableUserID(found, candidate.User.ID), input.Email, false, "invalid_credentials", input.IPAddress, input.UserAgent)
	return domain.CreatedLoginSession{}, domain.ErrInvalidCredentials
}

ok, err := service.VerifyPassword(input.Password, candidate.PasswordHash)
if err != nil || !ok {
	_ = recordLoginAttempt(ctx, tx, candidate.User.ID, input.Email, false, "invalid_credentials", input.IPAddress, input.UserAgent)
	return domain.CreatedLoginSession{}, domain.ErrInvalidCredentials
}
```

Then resolve optional device, insert success attempt, insert session, insert history, update last login, commit, and return `domain.CreatedLoginSession`.

- [ ] **Step 4.5: Run focused storage tests and verify they pass**

Run:

```bash
cd services/auth-service && go test ./internal/storage -run TestPGXLoginStore -count=1
```

Expected: PASS.

## Task 5: HTTP Login Handler

**Files:**

- Modify: `services/auth-service/internal/http/auth_handler.go`
- Modify: `services/auth-service/internal/http/auth_handler_test.go`

- [ ] **Step 5.1: Write failing HTTP tests**

In `auth_handler_test.go`, extend `fakeAuthService` or add `fakeLoginService`:

```go
type fakeLoginService struct {
	result domain.LoginResult
	err error
	got domain.LoginInput
}

func (f *fakeLoginService) Login(_ context.Context, input domain.LoginInput) (domain.LoginResult, error) {
	f.got = input
	return f.result, f.err
}
```

Add tests:

- `TestAuthLogin_SuccessReturnsTokenAndSafeUser`
- `TestAuthLogin_InvalidCredentialsReturns401`
- `TestAuthLogin_UnknownOrLockedUsesGeneric401`
- `TestAuthLogin_InvalidJSONReturns400`
- `TestAuthLogin_TrailingJSONReturns400`
- `TestAuthLogin_OversizedBodyReturns413`
- `TestAuthLogin_ServiceNilReturns503`
- `TestAuthLogin_ResponseDoesNotLeakSensitiveFields`

The success body assertion must decode:

```go
var body struct {
	AccessToken string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType string `json:"token_type"`
	ExpiresIn int `json:"expires_in"`
	User struct {
		ID string `json:"id"`
		Email string `json:"email"`
		DisplayName string `json:"display_name"`
		MustChangePassword bool `json:"must_change_password"`
	} `json:"user"`
}
```

- [ ] **Step 5.2: Run focused HTTP tests and verify they fail**

Run:

```bash
cd services/auth-service && go test ./internal/http -run TestAuthLogin -count=1
```

Expected: FAIL because `AuthLogin` and route support do not exist.

- [ ] **Step 5.3: Implement login request/response handler**

In `services/auth-service/internal/http/auth_handler.go`, add:

```go
const errCodeInvalidCredentials = "invalid_credentials"

type loginRequest struct {
	Email             string `json:"email"`
	Password          string `json:"password"`
	DeviceFingerprint string `json:"device_fingerprint"`
	DeviceName        string `json:"device_name"`
	Platform          string `json:"platform"`
}

type loginUserResponse struct {
	ID                 string `json:"id"`
	Email              string `json:"email"`
	DisplayName        string `json:"display_name"`
	MustChangePassword bool   `json:"must_change_password"`
}

type loginResponse struct {
	AccessToken  string            `json:"access_token"`
	RefreshToken string            `json:"refresh_token"`
	TokenType    string            `json:"token_type"`
	ExpiresIn    int               `json:"expires_in"`
	User         loginUserResponse `json:"user"`
}
```

Add `AuthLogin(login service.LoginManager) http.Handler` that:

- returns 503 when `login == nil`
- uses the existing 4 KiB `decodeAuthRequest` helper
- passes `RemoteAddr` and `User-Agent` into `domain.LoginInput`
- maps `ErrInvalidCredentials` to `401 invalid_credentials`
- writes bare token JSON like `/auth/refresh`

- [ ] **Step 5.4: Run focused HTTP tests and verify they pass**

Run:

```bash
cd services/auth-service && go test ./internal/http -run TestAuthLogin -count=1
```

Expected: PASS.

## Task 6: Router And App Wiring

**Files:**

- Modify: `services/auth-service/internal/http/routes.go`
- Modify: `services/auth-service/internal/http/router.go`
- Modify: `services/auth-service/internal/http/router_test.go`
- Modify: `services/auth-service/internal/app/app.go`
- Modify: `services/auth-service/internal/app/app_test.go`

- [ ] **Step 6.1: Write failing router tests**

Add to `router_test.go`:

```go
type routerLoginStub struct{}

func (routerLoginStub) Login(_ context.Context, _ domain.LoginInput) (domain.LoginResult, error) {
	return domain.LoginResult{AccessToken: "access-token", RefreshToken: "refresh-token", TokenType: "Bearer", ExpiresIn: 900, User: domain.LoginUser{ID: "user-1", Email: "user@example.com", DisplayName: "User"}}, nil
}
```

Update `NewRouter` test calls to include login as a fifth parameter. Add:

- `TestAuthLoginMethodNotAllowed`
- `TestAuthLoginRateLimiterAllowsRequestsUnderLimit`
- `TestAuthLoginRateLimiterRejectsRequestsOverLimit`

- [ ] **Step 6.2: Run router tests and verify they fail**

Run:

```bash
cd services/auth-service && go test ./internal/http -run 'TestAuthLogin|TestMethodAndNotFoundBehavior' -count=1
```

Expected: FAIL until route and router signature are updated.

- [ ] **Step 6.3: Wire route and app**

Modify `routes.go`:

```go
RouteAuthLogin = "/auth/login"
```

Change `NewRouter` signature:

```go
func NewRouter(cfg config.Config, logger *slog.Logger, users service.UserCreator, auth service.AuthSessionManager, login service.LoginManager) http.Handler
```

Register:

```go
mux.Handle(RouteAuthLogin, httputil.MethodNotAllowed(http.MethodPost, tokenEndpointLimiter.Middleware(AuthLogin(login))))
```

In `app.go`, add:

```go
var login service.LoginManager
```

When both `pool` and `tokens` are valid:

```go
auth = service.NewAuthService(tokens, storage.NewPGXSessionStore(pool))
login = service.NewLoginService(tokens, storage.NewPGXLoginStore(pool))
```

Pass `login` into `NewRouter`.

- [ ] **Step 6.4: Run router and app tests**

Run:

```bash
cd services/auth-service && go test ./internal/http ./internal/app -count=1
```

Expected: PASS.

## Task 7: Documentation

**Files:**

- Create: `docs/runbooks/task-email-password-login.md`
- Modify: `README.md`

- [ ] **Step 7.1: Write runbook**

Create `docs/runbooks/task-email-password-login.md` with sections:

- Overview
- Endpoint
- Temporary lockout
- Device handling
- Environment variables
- Security notes
- Known limitations
- Manual integration
- Validation
- Related and traceability

The runbook must explicitly state:

```markdown
Automatic brute-force lockout does not set `auth.users.status = 'locked'`.
That status is reserved for a future administrative/manual lock flow.
This task has no unlock endpoint or unlock UI; lockout expires by policy time.
```

- [ ] **Step 7.2: Update README auth section**

Update the service endpoint table to include `/auth/login`. Add an "Email/password login" subsection before JWT refresh/logout with:

- endpoint `POST /auth/login`
- temporary lockout policy columns and defaults
- body cap and rate limit
- runbook link
- out-of-scope note

- [ ] **Step 7.3: Run docs format check**

Run:

```bash
pnpm format:check:docs
```

Expected: PASS.

## Task 8: Full Verification, Review, And Final Commit

**Files:**

- All changed files

- [ ] **Step 8.1: Run Go formatting**

Run:

```bash
gofmt -w services/auth-service/internal/domain services/auth-service/internal/service services/auth-service/internal/storage services/auth-service/internal/http services/auth-service/internal/app
```

Expected: command exits 0.

- [ ] **Step 8.2: Run focused auth-service tests**

Run:

```bash
cd services/auth-service && go test ./...
```

Expected: PASS.

- [ ] **Step 8.3: Run coverage threshold**

Run:

```bash
pnpm test:coverage:go:check
```

Expected: "Go coverage threshold passed."

- [ ] **Step 8.4: Run required validation gates**

Run:

```bash
pnpm format:check
pnpm run ci
make ci
```

Expected: all pass. If Node 22 emits an engine warning, record it; only a nonzero exit blocks completion.

- [ ] **Step 8.5: Run security checks**

Run:

```bash
pnpm security:secrets
semgrep scan --config p/owasp-top-ten --config p/secrets services/auth-service migrations/auth docs/runbooks/task-email-password-login.md README.md
```

Expected: no findings that expose credentials, tokens, password hashes, token hashes, raw device fingerprints, or auth bypass.

- [ ] **Step 8.6: Perform code review and security review**

Use the `code-review` skill to review the final diff for correctness, regressions, and tests. Use the `security-code-review` skill for public endpoint, auth, secret, and brute-force concerns. Fix valid findings before completion.

- [ ] **Step 8.7: Commit implementation**

Run:

```bash
git status --short
git add migrations/auth/000003_auth_login_policy_window.up.sql migrations/auth/000003_auth_login_policy_window.down.sql services/auth-service docs/runbooks/task-email-password-login.md README.md docs/superpowers/plans/2026-05-27-auth-email-password-login.md
git commit -m "feat(auth): add email password login"
```

Expected: commit succeeds. Do not merge.

## Plan Self-Review

Spec coverage: covered by Tasks 1-8. Migration, generic errors, temporary lockout, dummy verification, device hashing, max devices, session/history creation, body cap, rate limit, docs, validation, code review, and security review all have tasks.

Placeholder scan: passed. The plan contains no TBD, TODO, "implement later", or unassigned scope.

Type consistency: passed. `LoginInput`, `LoginUser`, `LoginResult`, `CreateSessionInput`, and `CreatedLoginSession` are introduced before use.
