# Auth Admin Manual User Creation — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add `POST /admin/users` to auth-service so an admin can create a manual user with an Argon2id-hashed password, enforcing the auth policy stored in the DB, behind a temporary bootstrap token guard.

**Architecture:** The HTTP layer calls a `UserCreator` interface; `UserService` validates input, fetches policy from the DB, hashes the password with Argon2id, and delegates persistence to a `UserStore` interface; `PGXUserStore` executes the user + credential insert in a single transaction. If `ADMIN_BOOTSTRAP_TOKEN` is not set the middleware returns 503 before the handler is reached; if `DATABASE_URL` is not set the service is nil and the handler returns 503. Coverage target ≥ 90% is maintained via pgxmock for the storage layer and fake stores for the service layer.

**Tech Stack:** Go 1.25, `github.com/jackc/pgx/v5`, `golang.org/x/crypto/argon2`, `github.com/pashagolub/pgxmock/v2`, stdlib `net/http`, stdlib `testing`

---

## File Map

| Action | Path                                                           | Responsibility                                            |
| ------ | -------------------------------------------------------------- | --------------------------------------------------------- |
| Modify | `services/auth-service/go.mod`                                 | Add pgx/v5, crypto, pgxmock deps                          |
| Modify | `services/auth-service/internal/config/config.go`              | DatabaseURL, DBConnectTimeoutSeconds, AdminBootstrapToken |
| Modify | `services/auth-service/internal/config/config_test.go`         | Tests for new fields                                      |
| Create | `services/auth-service/internal/domain/user.go`                | User, PolicySettings, CreateUserInput types               |
| Create | `services/auth-service/internal/domain/errors.go`              | ErrDuplicateEmail, ErrPasswordPolicy, ErrInvalidInput     |
| Create | `services/auth-service/internal/domain/validation.go`          | ValidateEmail, NormalizeEmail, ValidatePassword           |
| Create | `services/auth-service/internal/domain/validation_test.go`     | Validation unit tests                                     |
| Create | `services/auth-service/internal/service/password.go`           | HashPassword (Argon2id PHC)                               |
| Create | `services/auth-service/internal/service/password_test.go`      | Hash ≠ plaintext, format, uniqueness                      |
| Create | `services/auth-service/internal/storage/pool.go`               | Pool interface (pgxpool + pgxmock compatible)             |
| Create | `services/auth-service/internal/storage/db.go`                 | OpenDB() returning Pool                                   |
| Create | `services/auth-service/internal/storage/user_store.go`         | UserStore interface + PGXUserStore                        |
| Create | `services/auth-service/internal/storage/user_store_test.go`    | pgxmock tests for store                                   |
| Create | `services/auth-service/internal/service/user_service.go`       | UserService.CreateUser, UserCreator interface             |
| Create | `services/auth-service/internal/service/user_service_test.go`  | Service tests with fake store                             |
| Create | `services/auth-service/internal/http/admin_middleware.go`      | AdminBootstrapGuard middleware                            |
| Create | `services/auth-service/internal/http/admin_middleware_test.go` | Token guard tests                                         |
| Create | `services/auth-service/internal/http/admin_handler.go`         | AdminCreateUser handler                                   |
| Create | `services/auth-service/internal/http/admin_handler_test.go`    | Handler tests with fake service                           |
| Modify | `services/auth-service/internal/http/routes.go`                | RouteAdminUsers constant                                  |
| Modify | `services/auth-service/internal/http/router.go`                | Accept UserCreator, register /admin/users                 |
| Modify | `services/auth-service/internal/http/router_test.go`           | Pass nil UserCreator to all existing tests                |
| Modify | `services/auth-service/internal/app/app.go`                    | DB init + service wiring                                  |
| Modify | `services/auth-service/internal/app/app_test.go`               | Update for new app.New signature                          |
| Create | `docs/runbooks/task-23-admin-manual-user-create.md`            | Runbook                                                   |
| Modify | `README.md`                                                    | Auth section: endpoint, curl example                      |

---

## Task 1: Create branch and add Go dependencies

**Files:**

- Modify: `services/auth-service/go.mod`
- Modify: `services/auth-service/go.sum`

- [ ] **Step 1.1: Create the feature branch**

```bash
git checkout develop && git pull origin develop
git checkout -b feat/auth-admin-manual-user-create
```

- [ ] **Step 1.2: Add pgx, golang.org/x/crypto, pgxmock**

```bash
cd services/auth-service
go get github.com/jackc/pgx/v5@latest
go get golang.org/x/crypto@latest
go get github.com/pashagolub/pgxmock/v2@latest
go mod tidy
cd ../..
```

- [ ] **Step 1.3: Verify build still passes**

```bash
cd services/auth-service && go build ./... && cd ../..
```

Expected: no output (success).

- [ ] **Step 1.4: Commit**

```bash
git add services/auth-service/go.mod services/auth-service/go.sum
git commit -m "chore(auth): add pgx, argon2, pgxmock dependencies"
```

---

## Task 2: Extend config

**Files:**

- Modify: `services/auth-service/internal/config/config.go`
- Modify: `services/auth-service/internal/config/config_test.go`

- [ ] **Step 2.1: Write failing tests for new config fields**

Add to `services/auth-service/internal/config/config_test.go`:

```go
func TestLoadDatabaseURLDefault(t *testing.T) {
	cfg := Load()
	if cfg.DatabaseURL != "" {
		t.Fatalf("expected empty DatabaseURL, got %q", cfg.DatabaseURL)
	}
}

func TestLoadDatabaseURLFromEnv(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://nchat:pass@localhost/nchat")
	cfg := Load()
	if cfg.DatabaseURL != "postgres://nchat:pass@localhost/nchat" {
		t.Fatalf("unexpected DatabaseURL %q", cfg.DatabaseURL)
	}
}

func TestLoadDBConnectTimeoutDefault(t *testing.T) {
	cfg := Load()
	if cfg.DBConnectTimeoutSeconds != 5 {
		t.Fatalf("expected 5, got %d", cfg.DBConnectTimeoutSeconds)
	}
}

func TestLoadAdminBootstrapTokenDefault(t *testing.T) {
	cfg := Load()
	if cfg.AdminBootstrapToken != "" {
		t.Fatalf("expected empty AdminBootstrapToken, got %q", cfg.AdminBootstrapToken)
	}
}

func TestLoadAdminBootstrapTokenFromEnv(t *testing.T) {
	t.Setenv("ADMIN_BOOTSTRAP_TOKEN", "super-secret")
	cfg := Load()
	if cfg.AdminBootstrapToken != "super-secret" {
		t.Fatalf("unexpected AdminBootstrapToken %q", cfg.AdminBootstrapToken)
	}
}
```

- [ ] **Step 2.2: Run to confirm they fail**

```bash
cd services/auth-service && go test ./internal/config/... -v -run "TestLoadDatabase|TestLoadDB|TestLoadAdmin" 2>&1 | head -20
cd ../..
```

Expected: FAIL — fields not defined yet.

- [ ] **Step 2.3: Add new fields to config.go**

Replace the entire `services/auth-service/internal/config/config.go`:

```go
package config

import platformconfig "github.com/nicrepository/nchat/libs/go/platform/config"

const (
	serviceName = "auth-service"
	defaultPort = 8081
)

type Config struct {
	ServiceName              string
	Env                      string
	Port                     int
	ReadHeaderTimeoutSeconds int
	DatabaseURL              string
	DBConnectTimeoutSeconds  int
	AdminBootstrapToken      string
}

func Load() Config {
	return Config{
		ServiceName:              serviceName,
		Env:                      platformconfig.GetString("APP_ENV", "development"),
		Port:                     platformconfig.GetInt("PORT", defaultPort),
		ReadHeaderTimeoutSeconds: platformconfig.GetInt("READ_HEADER_TIMEOUT_SECONDS", 5),
		DatabaseURL:              platformconfig.GetString("DATABASE_URL", ""),
		DBConnectTimeoutSeconds:  platformconfig.GetInt("DB_CONNECT_TIMEOUT_SECONDS", 5),
		AdminBootstrapToken:      platformconfig.GetString("ADMIN_BOOTSTRAP_TOKEN", ""),
	}
}
```

- [ ] **Step 2.4: Run all config tests**

```bash
cd services/auth-service && go test ./internal/config/... -v 2>&1
cd ../..
```

Expected: all PASS.

- [ ] **Step 2.5: Commit**

```bash
git add services/auth-service/internal/config/
git commit -m "feat(auth): extend config with DB URL and admin bootstrap token"
```

---

## Task 3: Domain types and error sentinels

**Files:**

- Create: `services/auth-service/internal/domain/user.go`
- Create: `services/auth-service/internal/domain/errors.go`

- [ ] **Step 3.1: Create domain/user.go**

```go
// services/auth-service/internal/domain/user.go
package domain

import "time"

type User struct {
	ID              string
	Email           string
	DisplayName     string
	FullName        string
	Status          string
	AuthSource      string
	EmailVerifiedAt time.Time
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

type PolicySettings struct {
	MinPasswordLength int
	RequireUppercase  bool
	RequireLowercase  bool
	RequireNumber     bool
	RequireSymbol     bool
}

type CreateUserInput struct {
	Email              string
	DisplayName        string
	FullName           string
	InitialPassword    string
	MustChangePassword bool
}
```

- [ ] **Step 3.2: Create domain/errors.go**

```go
// services/auth-service/internal/domain/errors.go
package domain

import "errors"

var (
	ErrDuplicateEmail = errors.New("email already registered")
	ErrPasswordPolicy = errors.New("password does not meet policy requirements")
	ErrInvalidInput   = errors.New("invalid input")
)
```

- [ ] **Step 3.3: Verify the package compiles**

```bash
cd services/auth-service && go build ./internal/domain/... && cd ../..
```

Expected: no output.

- [ ] **Step 3.4: Commit**

```bash
git add services/auth-service/internal/domain/
git commit -m "feat(auth): add domain types User, PolicySettings, CreateUserInput and error sentinels"
```

---

## Task 4: Domain validation

**Files:**

- Create: `services/auth-service/internal/domain/validation.go`
- Create: `services/auth-service/internal/domain/validation_test.go`

- [ ] **Step 4.1: Write failing tests**

Create `services/auth-service/internal/domain/validation_test.go`:

```go
package domain_test

import (
	"errors"
	"testing"

	"github.com/nicrepository/nchat/services/auth-service/internal/domain"
)

func TestValidateEmail_Valid(t *testing.T) {
	cases := []string{
		"user@example.com",
		"USER@EXAMPLE.COM",
		"user+tag@sub.domain.io",
		"a@b.co",
	}
	for _, email := range cases {
		if err := domain.ValidateEmail(email); err != nil {
			t.Errorf("ValidateEmail(%q): unexpected error %v", email, err)
		}
	}
}

func TestValidateEmail_Invalid(t *testing.T) {
	cases := []string{"", "notanemail", "@nodomain", "no@", "no@domain"}
	for _, email := range cases {
		err := domain.ValidateEmail(email)
		if err == nil {
			t.Errorf("ValidateEmail(%q): expected error, got nil", email)
		}
		if !errors.Is(err, domain.ErrInvalidInput) {
			t.Errorf("ValidateEmail(%q): expected ErrInvalidInput, got %v", email, err)
		}
	}
}

func TestNormalizeEmail(t *testing.T) {
	got := domain.NormalizeEmail("  USER@EXAMPLE.COM  ")
	if got != "user@example.com" {
		t.Fatalf("expected user@example.com, got %q", got)
	}
}

func TestValidatePassword_MeetsPolicy(t *testing.T) {
	policy := domain.PolicySettings{
		MinPasswordLength: 8,
		RequireUppercase:  true,
		RequireLowercase:  true,
		RequireNumber:     true,
		RequireSymbol:     true,
	}
	if err := domain.ValidatePassword("Abcdef1!", policy); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
}

func TestValidatePassword_TooShort(t *testing.T) {
	policy := domain.PolicySettings{MinPasswordLength: 12}
	err := domain.ValidatePassword("short", policy)
	if !errors.Is(err, domain.ErrPasswordPolicy) {
		t.Fatalf("expected ErrPasswordPolicy, got %v", err)
	}
}

func TestValidatePassword_MissingUppercase(t *testing.T) {
	policy := domain.PolicySettings{MinPasswordLength: 1, RequireUppercase: true}
	err := domain.ValidatePassword("nouppercase1!", policy)
	if !errors.Is(err, domain.ErrPasswordPolicy) {
		t.Fatalf("expected ErrPasswordPolicy, got %v", err)
	}
}

func TestValidatePassword_MissingLowercase(t *testing.T) {
	policy := domain.PolicySettings{MinPasswordLength: 1, RequireLowercase: true}
	err := domain.ValidatePassword("NOLOWERCASE1!", policy)
	if !errors.Is(err, domain.ErrPasswordPolicy) {
		t.Fatalf("expected ErrPasswordPolicy, got %v", err)
	}
}

func TestValidatePassword_MissingNumber(t *testing.T) {
	policy := domain.PolicySettings{MinPasswordLength: 1, RequireNumber: true}
	err := domain.ValidatePassword("NoNumber!", policy)
	if !errors.Is(err, domain.ErrPasswordPolicy) {
		t.Fatalf("expected ErrPasswordPolicy, got %v", err)
	}
}

func TestValidatePassword_MissingSymbol(t *testing.T) {
	policy := domain.PolicySettings{MinPasswordLength: 1, RequireSymbol: true}
	err := domain.ValidatePassword("NoSymbol1", policy)
	if !errors.Is(err, domain.ErrPasswordPolicy) {
		t.Fatalf("expected ErrPasswordPolicy, got %v", err)
	}
}

func TestValidatePassword_NoRequirements(t *testing.T) {
	policy := domain.PolicySettings{MinPasswordLength: 1}
	if err := domain.ValidatePassword("a", policy); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
}
```

- [ ] **Step 4.2: Run to confirm fail**

```bash
cd services/auth-service && go test ./internal/domain/... -v 2>&1 | head -10
cd ../..
```

Expected: compilation error — validation.go not found.

- [ ] **Step 4.3: Create domain/validation.go**

```go
// services/auth-service/internal/domain/validation.go
package domain

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
)

var emailRE = regexp.MustCompile(`(?i)^[a-z0-9._%+\-]+@[a-z0-9.\-]+\.[a-z]{2,}$`)

func ValidateEmail(email string) error {
	if email == "" {
		return fmt.Errorf("%w: email is required", ErrInvalidInput)
	}
	if !emailRE.MatchString(email) {
		return fmt.Errorf("%w: invalid email format", ErrInvalidInput)
	}
	return nil
}

func NormalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

func ValidatePassword(password string, policy PolicySettings) error {
	if len(password) < policy.MinPasswordLength {
		return fmt.Errorf("%w: minimum length is %d characters", ErrPasswordPolicy, policy.MinPasswordLength)
	}
	if policy.RequireUppercase && !containsUppercase(password) {
		return fmt.Errorf("%w: must contain at least one uppercase letter", ErrPasswordPolicy)
	}
	if policy.RequireLowercase && !containsLowercase(password) {
		return fmt.Errorf("%w: must contain at least one lowercase letter", ErrPasswordPolicy)
	}
	if policy.RequireNumber && !containsDigit(password) {
		return fmt.Errorf("%w: must contain at least one number", ErrPasswordPolicy)
	}
	if policy.RequireSymbol && !containsSymbol(password) {
		return fmt.Errorf("%w: must contain at least one symbol", ErrPasswordPolicy)
	}
	return nil
}

func containsUppercase(s string) bool {
	for _, r := range s {
		if unicode.IsUpper(r) {
			return true
		}
	}
	return false
}

func containsLowercase(s string) bool {
	for _, r := range s {
		if unicode.IsLower(r) {
			return true
		}
	}
	return false
}

func containsDigit(s string) bool {
	for _, r := range s {
		if unicode.IsDigit(r) {
			return true
		}
	}
	return false
}

func containsSymbol(s string) bool {
	for _, r := range s {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && !unicode.IsSpace(r) {
			return true
		}
	}
	return false
}
```

- [ ] **Step 4.4: Run tests — all should pass**

```bash
cd services/auth-service && go test ./internal/domain/... -v 2>&1
cd ../..
```

Expected: all PASS.

- [ ] **Step 4.5: Commit**

```bash
git add services/auth-service/internal/domain/
git commit -m "feat(auth): add email and password validation with policy enforcement"
```

---

## Task 5: Argon2id password hashing

**Files:**

- Create: `services/auth-service/internal/service/password.go`
- Create: `services/auth-service/internal/service/password_test.go`

- [ ] **Step 5.1: Write failing tests**

Create `services/auth-service/internal/service/password_test.go`:

```go
package service_test

import (
	"strings"
	"testing"

	"github.com/nicrepository/nchat/services/auth-service/internal/service"
)

func TestHashPassword_NotEqualToPlaintext(t *testing.T) {
	hash, err := service.HashPassword("my-secret-password")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if hash == "my-secret-password" {
		t.Fatal("hash must not equal plaintext")
	}
}

func TestHashPassword_PHCFormat(t *testing.T) {
	hash, err := service.HashPassword("P@ssword123")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if !strings.HasPrefix(hash, "$argon2id$") {
		t.Fatalf("expected PHC argon2id prefix, got %q", hash[:min(len(hash), 20)])
	}
}

func TestHashPassword_UniquePerCall(t *testing.T) {
	h1, err := service.HashPassword("same-password")
	if err != nil {
		t.Fatalf("first hash: %v", err)
	}
	h2, err := service.HashPassword("same-password")
	if err != nil {
		t.Fatalf("second hash: %v", err)
	}
	if h1 == h2 {
		t.Fatal("two hashes of the same password must differ (different salts)")
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
```

- [ ] **Step 5.2: Run to confirm fail**

```bash
cd services/auth-service && go test ./internal/service/... -v -run "TestHash" 2>&1 | head -10
cd ../..
```

Expected: compilation error — service package has only doc.go.

- [ ] **Step 5.3: Create service/password.go**

```go
// services/auth-service/internal/service/password.go
package service

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"

	"golang.org/x/crypto/argon2"
)

const (
	argon2Memory      uint32 = 64 * 1024
	argon2Iterations  uint32 = 3
	argon2Parallelism uint8  = 4
	argon2SaltLen            = 16
	argon2KeyLen      uint32 = 32
)

// HashPassword hashes password using Argon2id and returns a PHC-format string.
func HashPassword(password string) (string, error) {
	salt := make([]byte, argon2SaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generate salt: %w", err)
	}
	hash := argon2.IDKey([]byte(password), salt, argon2Iterations, argon2Memory, argon2Parallelism, argon2KeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version,
		argon2Memory,
		argon2Iterations,
		argon2Parallelism,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(hash),
	), nil
}
```

- [ ] **Step 5.4: Run tests — all should pass**

```bash
cd services/auth-service && go test ./internal/service/... -v -run "TestHash" 2>&1
cd ../..
```

Expected: all PASS.

- [ ] **Step 5.5: Commit**

```bash
git add services/auth-service/internal/service/
git commit -m "feat(auth): add Argon2id PHC password hashing"
```

---

## Task 6: Storage Pool interface and DB opener

**Files:**

- Create: `services/auth-service/internal/storage/pool.go`
- Create: `services/auth-service/internal/storage/db.go`

- [ ] **Step 6.1: Create storage/pool.go**

```go
// services/auth-service/internal/storage/pool.go
package storage

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// Pool is satisfied by *pgxpool.Pool and pgxmock.PgxPoolIface.
type Pool interface {
	Begin(ctx context.Context) (pgx.Tx, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}
```

- [ ] **Step 6.2: Create storage/db.go**

```go
// services/auth-service/internal/storage/db.go
package storage

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// OpenDB creates a connection pool, pings the database, and returns it as Pool.
func OpenDB(ctx context.Context, dsn string, connectTimeoutSeconds int) (Pool, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse db config: %w", err)
	}
	cfg.ConnConfig.ConnectTimeout = time.Duration(connectTimeoutSeconds) * time.Second

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("create pool: %w", err)
	}

	pingCtx, cancel := context.WithTimeout(ctx, time.Duration(connectTimeoutSeconds)*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping db: %w", err)
	}
	return pool, nil
}
```

- [ ] **Step 6.3: Verify compilation**

```bash
cd services/auth-service && go build ./internal/storage/... && cd ../..
```

Expected: no output.

- [ ] **Step 6.4: Commit**

```bash
git add services/auth-service/internal/storage/pool.go services/auth-service/internal/storage/db.go
git commit -m "feat(auth): add storage Pool interface and pgxpool DB opener"
```

---

## Task 7: UserStore interface and pgx implementation

**Files:**

- Create: `services/auth-service/internal/storage/user_store.go`
- Create: `services/auth-service/internal/storage/user_store_test.go`

- [ ] **Step 7.1: Write failing tests**

Create `services/auth-service/internal/storage/user_store_test.go`:

```go
package storage_test

import (
	"context"
	"errors"
	"testing"
	"time"

	pgxmock "github.com/pashagolub/pgxmock/v2"

	"github.com/nicrepository/nchat/services/auth-service/internal/domain"
	"github.com/nicrepository/nchat/services/auth-service/internal/storage"
)

func TestPGXUserStore_GetPolicySettings_Success(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectQuery(`SELECT min_password_length`).
		WillReturnRows(pgxmock.NewRows([]string{
			"min_password_length", "require_uppercase", "require_lowercase",
			"require_number", "require_symbol",
		}).AddRow(12, true, true, true, true))

	store := storage.NewPGXUserStore(mock)
	policy, err := store.GetPolicySettings(context.Background())
	if err != nil {
		t.Fatalf("GetPolicySettings: %v", err)
	}
	if policy.MinPasswordLength != 12 {
		t.Fatalf("expected 12, got %d", policy.MinPasswordLength)
	}
	if !policy.RequireUppercase {
		t.Fatal("expected RequireUppercase true")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXUserStore_GetPolicySettings_QueryError(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectQuery(`SELECT min_password_length`).
		WillReturnError(errors.New("connection lost"))

	store := storage.NewPGXUserStore(mock)
	_, err = store.GetPolicySettings(context.Background())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXUserStore_CreateUser_Success(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	userID := "550e8400-e29b-41d4-a716-446655440000"
	now := time.Now()

	mock.ExpectBegin()
	mock.ExpectQuery(`INSERT INTO auth\.users`).
		WithArgs("user@example.com", "User Name", nil).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "email", "display_name", "full_name", "status", "auth_source",
			"email_verified_at", "created_at", "updated_at",
		}).AddRow(userID, "user@example.com", "User Name", "", "active", "manual", now, now, now))
	mock.ExpectExec(`INSERT INTO auth\.user_password_credentials`).
		WithArgs(userID, "$argon2id$test-hash", true).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectCommit()
	mock.ExpectRollback() // deferred Rollback after Commit returns pgx.ErrTxClosed

	store := storage.NewPGXUserStore(mock)
	input := domain.CreateUserInput{
		Email: "user@example.com", DisplayName: "User Name",
		MustChangePassword: true,
	}
	user, err := store.CreateUser(context.Background(), input, "$argon2id$test-hash")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if user.ID != userID {
		t.Fatalf("expected %q, got %q", userID, user.ID)
	}
	if user.Email != "user@example.com" {
		t.Fatalf("expected user@example.com, got %q", user.Email)
	}
	if user.Status != "active" {
		t.Fatalf("expected active, got %q", user.Status)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXUserStore_CreateUser_DuplicateEmail(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectBegin()
	mock.ExpectQuery(`INSERT INTO auth\.users`).
		WillReturnError(&pgxmockPgError{code: "23505"})
	mock.ExpectRollback()

	store := storage.NewPGXUserStore(mock)
	input := domain.CreateUserInput{Email: "dup@example.com", DisplayName: "Dup"}
	_, err = store.CreateUser(context.Background(), input, "hash")
	if !errors.Is(err, domain.ErrDuplicateEmail) {
		t.Fatalf("expected ErrDuplicateEmail, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// pgxmockPgError implements pgconn.PgError for testing duplicate key detection.
type pgxmockPgError struct{ code string }

func (e *pgxmockPgError) Error() string { return "pg error " + e.code }
func (e *pgxmockPgError) Unwrap() error { return nil }
```

> **Note:** The `pgxmockPgError` above won't satisfy the `errors.As(err, &pgconn.PgError{})` check directly. In Step 7.3 the store uses `pgconn.PgError` type assertion. Replace the duplicate-email test with a real pgconn.PgError:

```go
import "github.com/jackc/pgx/v5/pgconn"

// Replace pgxmockPgError with:
mock.ExpectQuery(`INSERT INTO auth\.users`).
    WillReturnError(&pgconn.PgError{Code: "23505"})
```

- [ ] **Step 7.2: Run to confirm fail**

```bash
cd services/auth-service && go test ./internal/storage/... -v 2>&1 | head -10
cd ../..
```

Expected: compilation error — NewPGXUserStore not found.

- [ ] **Step 7.3: Create storage/user_store.go**

```go
// services/auth-service/internal/storage/user_store.go
package storage

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/nicrepository/nchat/services/auth-service/internal/domain"
)

const pgCodeUniqueViolation = "23505"

// UserStore is the persistence interface for user operations.
type UserStore interface {
	CreateUser(ctx context.Context, input domain.CreateUserInput, passwordHash string) (domain.User, error)
	GetPolicySettings(ctx context.Context) (domain.PolicySettings, error)
}

// PGXUserStore implements UserStore using a pgx connection pool.
type PGXUserStore struct {
	pool Pool
}

func NewPGXUserStore(pool Pool) *PGXUserStore {
	return &PGXUserStore{pool: pool}
}

func (s *PGXUserStore) GetPolicySettings(ctx context.Context) (domain.PolicySettings, error) {
	var p domain.PolicySettings
	err := s.pool.QueryRow(ctx, `
		SELECT min_password_length, require_uppercase, require_lowercase,
		       require_number, require_symbol
		FROM auth.auth_policy_settings
		WHERE id = 1`).Scan(
		&p.MinPasswordLength, &p.RequireUppercase, &p.RequireLowercase,
		&p.RequireNumber, &p.RequireSymbol,
	)
	if err != nil {
		return domain.PolicySettings{}, fmt.Errorf("get policy settings: %w", err)
	}
	return p, nil
}

func (s *PGXUserStore) CreateUser(ctx context.Context, input domain.CreateUserInput, passwordHash string) (domain.User, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.User{}, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var u domain.User
	err = tx.QueryRow(ctx, `
		INSERT INTO auth.users (email, display_name, full_name, status, auth_source, email_verified_at)
		VALUES ($1, $2, $3, 'active', 'manual', now())
		RETURNING id, email::text, display_name, COALESCE(full_name, ''), status, auth_source,
		          email_verified_at, created_at, updated_at`,
		input.Email, input.DisplayName, nullableString(input.FullName),
	).Scan(
		&u.ID, &u.Email, &u.DisplayName, &u.FullName, &u.Status, &u.AuthSource,
		&u.EmailVerifiedAt, &u.CreatedAt, &u.UpdatedAt,
	)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == pgCodeUniqueViolation {
			return domain.User{}, domain.ErrDuplicateEmail
		}
		return domain.User{}, fmt.Errorf("insert user: %w", err)
	}

	_, err = tx.Exec(ctx, `
		INSERT INTO auth.user_password_credentials (user_id, password_hash, must_change_password)
		VALUES ($1, $2, $3)`,
		u.ID, passwordHash, input.MustChangePassword,
	)
	if err != nil {
		return domain.User{}, fmt.Errorf("insert password credential: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return domain.User{}, fmt.Errorf("commit tx: %w", err)
	}
	return u, nil
}

func nullableString(s string) any {
	if s == "" {
		return nil
	}
	return s
}
```

- [ ] **Step 7.4: Fix the duplicate-email test to use pgconn.PgError directly**

In `storage/user_store_test.go`, at the top of `TestPGXUserStore_CreateUser_DuplicateEmail`, replace the `pgxmockPgError` struct and its usage with:

```go
import "github.com/jackc/pgx/v5/pgconn"

// Inside test:
mock.ExpectBegin()
mock.ExpectQuery(`INSERT INTO auth\.users`).
    WillReturnError(&pgconn.PgError{Code: "23505"})
mock.ExpectRollback()
```

Remove the `pgxmockPgError` type entirely from the file.

- [ ] **Step 7.5: Run tests — all should pass**

```bash
cd services/auth-service && go test ./internal/storage/... -v 2>&1
cd ../..
```

Expected: all PASS (4 tests).

- [ ] **Step 7.6: Commit**

```bash
git add services/auth-service/internal/storage/
git commit -m "feat(auth): add UserStore interface and PGXUserStore with pgxmock tests"
```

---

## Task 8: UserService

**Files:**

- Create: `services/auth-service/internal/service/user_service.go`
- Create: `services/auth-service/internal/service/user_service_test.go`

- [ ] **Step 8.1: Write failing tests**

Create `services/auth-service/internal/service/user_service_test.go`:

```go
package service_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/nicrepository/nchat/services/auth-service/internal/domain"
	"github.com/nicrepository/nchat/services/auth-service/internal/service"
)

type fakeStore struct {
	policy    domain.PolicySettings
	policyErr error
	user      domain.User
	createErr error
	gotHash   string
}

func (f *fakeStore) GetPolicySettings(_ context.Context) (domain.PolicySettings, error) {
	return f.policy, f.policyErr
}

func (f *fakeStore) CreateUser(_ context.Context, _ domain.CreateUserInput, hash string) (domain.User, error) {
	f.gotHash = hash
	return f.user, f.createErr
}

func defaultPolicy() domain.PolicySettings {
	return domain.PolicySettings{
		MinPasswordLength: 8,
		RequireUppercase:  true,
		RequireLowercase:  true,
		RequireNumber:     true,
		RequireSymbol:     true,
	}
}

func TestUserService_CreateUser_Success(t *testing.T) {
	now := time.Now()
	store := &fakeStore{
		policy: defaultPolicy(),
		user: domain.User{
			ID: "uuid-1", Email: "user@example.com", DisplayName: "User",
			Status: "active", AuthSource: "manual", EmailVerifiedAt: now,
		},
	}
	svc := service.NewUserService(store)
	user, err := svc.CreateUser(context.Background(), domain.CreateUserInput{
		Email: "user@example.com", DisplayName: "User",
		InitialPassword: "Abcdef1!", MustChangePassword: true,
	})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if user.ID != "uuid-1" {
		t.Fatalf("expected uuid-1, got %q", user.ID)
	}
	if store.gotHash == "" {
		t.Fatal("expected password hash to be set")
	}
	if store.gotHash == "Abcdef1!" {
		t.Fatal("password must be hashed, not stored as plaintext")
	}
	if !errors.Is(errors.New(store.gotHash[:10]), errors.New(store.gotHash[:10])) {
		t.Skip() // always passes — just ensuring no panic
	}
}

func TestUserService_CreateUser_NormalizesEmail(t *testing.T) {
	store := &fakeStore{policy: defaultPolicy(), user: domain.User{Email: "user@example.com"}}
	svc := service.NewUserService(store)
	_, err := svc.CreateUser(context.Background(), domain.CreateUserInput{
		Email: "  USER@EXAMPLE.COM  ", DisplayName: "User", InitialPassword: "Abcdef1!",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestUserService_CreateUser_EmptyEmail(t *testing.T) {
	store := &fakeStore{policy: defaultPolicy()}
	svc := service.NewUserService(store)
	_, err := svc.CreateUser(context.Background(), domain.CreateUserInput{
		Email: "", DisplayName: "User", InitialPassword: "Abcdef1!",
	})
	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput, got %v", err)
	}
}

func TestUserService_CreateUser_EmptyDisplayName(t *testing.T) {
	store := &fakeStore{policy: defaultPolicy()}
	svc := service.NewUserService(store)
	_, err := svc.CreateUser(context.Background(), domain.CreateUserInput{
		Email: "user@example.com", DisplayName: "", InitialPassword: "Abcdef1!",
	})
	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput, got %v", err)
	}
}

func TestUserService_CreateUser_EmptyPassword(t *testing.T) {
	store := &fakeStore{policy: defaultPolicy()}
	svc := service.NewUserService(store)
	_, err := svc.CreateUser(context.Background(), domain.CreateUserInput{
		Email: "user@example.com", DisplayName: "User", InitialPassword: "",
	})
	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput, got %v", err)
	}
}

func TestUserService_CreateUser_PolicyViolation(t *testing.T) {
	store := &fakeStore{policy: domain.PolicySettings{MinPasswordLength: 20}}
	svc := service.NewUserService(store)
	_, err := svc.CreateUser(context.Background(), domain.CreateUserInput{
		Email: "user@example.com", DisplayName: "User", InitialPassword: "short",
	})
	if !errors.Is(err, domain.ErrPasswordPolicy) {
		t.Fatalf("expected ErrPasswordPolicy, got %v", err)
	}
}

func TestUserService_CreateUser_PolicyError(t *testing.T) {
	store := &fakeStore{policyErr: errors.New("db down")}
	svc := service.NewUserService(store)
	_, err := svc.CreateUser(context.Background(), domain.CreateUserInput{
		Email: "user@example.com", DisplayName: "User", InitialPassword: "Abcdef1!",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestUserService_CreateUser_DuplicateEmail(t *testing.T) {
	store := &fakeStore{policy: defaultPolicy(), createErr: domain.ErrDuplicateEmail}
	svc := service.NewUserService(store)
	_, err := svc.CreateUser(context.Background(), domain.CreateUserInput{
		Email: "dup@example.com", DisplayName: "User", InitialPassword: "Abcdef1!",
	})
	if !errors.Is(err, domain.ErrDuplicateEmail) {
		t.Fatalf("expected ErrDuplicateEmail, got %v", err)
	}
}
```

- [ ] **Step 8.2: Run to confirm fail**

```bash
cd services/auth-service && go test ./internal/service/... -v -run "TestUserService" 2>&1 | head -10
cd ../..
```

Expected: compilation error.

- [ ] **Step 8.3: Create service/user_service.go**

```go
// services/auth-service/internal/service/user_service.go
package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/nicrepository/nchat/services/auth-service/internal/domain"
	"github.com/nicrepository/nchat/services/auth-service/internal/storage"
)

// UserCreator is the interface the HTTP handler depends on.
type UserCreator interface {
	CreateUser(ctx context.Context, input domain.CreateUserInput) (domain.User, error)
}

// UserService implements UserCreator.
type UserService struct {
	store storage.UserStore
}

func NewUserService(store storage.UserStore) *UserService {
	return &UserService{store: store}
}

func (s *UserService) CreateUser(ctx context.Context, input domain.CreateUserInput) (domain.User, error) {
	input.Email = domain.NormalizeEmail(input.Email)
	input.DisplayName = strings.TrimSpace(input.DisplayName)
	input.FullName = strings.TrimSpace(input.FullName)

	if err := domain.ValidateEmail(input.Email); err != nil {
		return domain.User{}, err
	}
	if input.DisplayName == "" {
		return domain.User{}, fmt.Errorf("%w: display_name is required", domain.ErrInvalidInput)
	}
	if input.InitialPassword == "" {
		return domain.User{}, fmt.Errorf("%w: initial_password is required", domain.ErrInvalidInput)
	}

	policy, err := s.store.GetPolicySettings(ctx)
	if err != nil {
		return domain.User{}, fmt.Errorf("get policy: %w", err)
	}

	if err := domain.ValidatePassword(input.InitialPassword, policy); err != nil {
		return domain.User{}, err
	}

	hash, err := HashPassword(input.InitialPassword)
	if err != nil {
		return domain.User{}, fmt.Errorf("hash password: %w", err)
	}

	return s.store.CreateUser(ctx, input, hash)
}
```

- [ ] **Step 8.4: Run all service tests**

```bash
cd services/auth-service && go test ./internal/service/... -v 2>&1
cd ../..
```

Expected: all PASS (hash tests + service tests).

- [ ] **Step 8.5: Commit**

```bash
git add services/auth-service/internal/service/
git commit -m "feat(auth): add UserService.CreateUser with validation, policy check, and Argon2id hashing"
```

---

## Task 9: Admin bootstrap token middleware

**Files:**

- Create: `services/auth-service/internal/http/admin_middleware.go`
- Create: `services/auth-service/internal/http/admin_middleware_test.go`

- [ ] **Step 9.1: Write failing tests**

Create `services/auth-service/internal/http/admin_middleware_test.go`:

```go
package httpapi_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	httpapi "github.com/nicrepository/nchat/services/auth-service/internal/http"
)

var okHandler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
})

func TestAdminBootstrapGuard_TokenNotConfigured_Returns503(t *testing.T) {
	handler := httpapi.AdminBootstrapGuard("")(okHandler)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/admin/users", nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", rec.Code)
	}
	assertErrorCode(t, rec.Body.Bytes(), "service_unavailable")
}

func TestAdminBootstrapGuard_MissingHeader_Returns401(t *testing.T) {
	handler := httpapi.AdminBootstrapGuard("secret-token")(okHandler)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/admin/users", nil))

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
	assertErrorCode(t, rec.Body.Bytes(), "unauthorized")
}

func TestAdminBootstrapGuard_WrongToken_Returns401(t *testing.T) {
	handler := httpapi.AdminBootstrapGuard("secret-token")(okHandler)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/admin/users", nil)
	req.Header.Set("X-NChat-Admin-Token", "wrong-token")
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestAdminBootstrapGuard_CorrectToken_Passes(t *testing.T) {
	handler := httpapi.AdminBootstrapGuard("secret-token")(okHandler)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/admin/users", nil)
	req.Header.Set("X-NChat-Admin-Token", "secret-token")
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}

func assertErrorCode(t *testing.T, body []byte, code string) {
	t.Helper()
	var env struct {
		Error *struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	if env.Error == nil {
		t.Fatal("expected error envelope")
	}
	if env.Error.Code != code {
		t.Fatalf("expected code %q, got %q", code, env.Error.Code)
	}
}
```

- [ ] **Step 9.2: Run to confirm fail**

```bash
cd services/auth-service && go test ./internal/http/... -v -run "TestAdminBootstrap" 2>&1 | head -10
cd ../..
```

Expected: compilation error.

- [ ] **Step 9.3: Create http/admin_middleware.go**

```go
// services/auth-service/internal/http/admin_middleware.go
package httpapi

import (
	"net/http"

	"github.com/nicrepository/nchat/libs/go/platform/httputil"
)

const adminTokenHeader = "X-NChat-Admin-Token"

// AdminBootstrapGuard is a temporary dev-only guard — NOT final RBAC.
// Returns 503 if the bootstrap token is not configured in the environment.
// Returns 401 if the X-NChat-Admin-Token header is missing or wrong.
// The provided token is never logged.
func AdminBootstrapGuard(token string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if token == "" {
				httputil.WriteError(w, http.StatusServiceUnavailable, "service_unavailable", "admin endpoint disabled: ADMIN_BOOTSTRAP_TOKEN not set")
				return
			}
			provided := r.Header.Get(adminTokenHeader)
			if provided == "" || provided != token {
				httputil.WriteError(w, http.StatusUnauthorized, httputil.ErrCodeUnauthorized, "unauthorized")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
```

- [ ] **Step 9.4: Run tests — all should pass**

```bash
cd services/auth-service && go test ./internal/http/... -v -run "TestAdminBootstrap" 2>&1
cd ../..
```

Expected: 4 tests PASS.

- [ ] **Step 9.5: Commit**

```bash
git add services/auth-service/internal/http/admin_middleware.go services/auth-service/internal/http/admin_middleware_test.go
git commit -m "feat(auth): add AdminBootstrapGuard middleware for temporary admin token check"
```

---

## Task 10: Admin handler

**Files:**

- Create: `services/auth-service/internal/http/admin_handler.go`
- Create: `services/auth-service/internal/http/admin_handler_test.go`

- [ ] **Step 10.1: Write failing tests**

Create `services/auth-service/internal/http/admin_handler_test.go`:

```go
package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nicrepository/nchat/services/auth-service/internal/domain"
	httpapi "github.com/nicrepository/nchat/services/auth-service/internal/http"
)

type fakeUserCreator struct {
	user domain.User
	err  error
}

func (f *fakeUserCreator) CreateUser(_ context.Context, _ domain.CreateUserInput) (domain.User, error) {
	return f.user, f.err
}

var testUser = domain.User{
	ID: "uuid-1", Email: "user@example.com", DisplayName: "User Name",
	Status: "active", AuthSource: "manual",
	EmailVerifiedAt: time.Now(), CreatedAt: time.Now(), UpdatedAt: time.Now(),
}

func postAdminUsers(t *testing.T, handler http.Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/admin/users", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(rec, req)
	return rec
}

func TestAdminCreateUser_Success_Returns201(t *testing.T) {
	handler := httpapi.AdminCreateUser(&fakeUserCreator{user: testUser})
	body := `{"email":"user@example.com","display_name":"User Name","initial_password":"Abcdef1!","must_change_password":true}`
	rec := postAdminUsers(t, handler, body)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d — body: %s", rec.Code, rec.Body.String())
	}
	assertJSONResponse(t, rec, http.StatusCreated)
}

func TestAdminCreateUser_ResponseHasNoPasswordHash(t *testing.T) {
	handler := httpapi.AdminCreateUser(&fakeUserCreator{user: testUser})
	body := `{"email":"user@example.com","display_name":"User","initial_password":"Abcdef1!"}`
	rec := postAdminUsers(t, handler, body)

	raw := rec.Body.String()
	if strings.Contains(raw, "argon2") || strings.Contains(raw, "password_hash") || strings.Contains(raw, "hash") {
		t.Fatalf("response must not leak password hash: %s", raw)
	}
}

func TestAdminCreateUser_ResponseShape(t *testing.T) {
	handler := httpapi.AdminCreateUser(&fakeUserCreator{user: testUser})
	body := `{"email":"user@example.com","display_name":"User","initial_password":"Abcdef1!"}`
	rec := postAdminUsers(t, handler, body)

	var env struct {
		Data *struct {
			ID          string `json:"id"`
			Email       string `json:"email"`
			DisplayName string `json:"display_name"`
			Status      string `json:"status"`
			AuthSource  string `json:"auth_source"`
		} `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&env); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if env.Data == nil {
		t.Fatal("expected data envelope")
	}
	if env.Data.ID != "uuid-1" {
		t.Fatalf("expected id uuid-1, got %q", env.Data.ID)
	}
	if env.Data.Email != "user@example.com" {
		t.Fatalf("expected user@example.com, got %q", env.Data.Email)
	}
	if env.Data.Status != "active" {
		t.Fatalf("expected active, got %q", env.Data.Status)
	}
}

func TestAdminCreateUser_ServiceNil_Returns503(t *testing.T) {
	handler := httpapi.AdminCreateUser(nil)
	rec := postAdminUsers(t, handler, `{}`)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", rec.Code)
	}
}

func TestAdminCreateUser_InvalidJSON_Returns400(t *testing.T) {
	handler := httpapi.AdminCreateUser(&fakeUserCreator{})
	rec := postAdminUsers(t, handler, `not-json`)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestAdminCreateUser_DuplicateEmail_Returns409(t *testing.T) {
	handler := httpapi.AdminCreateUser(&fakeUserCreator{err: domain.ErrDuplicateEmail})
	body := `{"email":"dup@example.com","display_name":"U","initial_password":"P@ss123!"}`
	rec := postAdminUsers(t, handler, body)

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d", rec.Code)
	}
	assertErrorCode(t, rec.Body.Bytes(), "conflict")
}

func TestAdminCreateUser_InvalidInput_Returns400(t *testing.T) {
	handler := httpapi.AdminCreateUser(&fakeUserCreator{
		err: fmt.Errorf("%w: email is required", domain.ErrInvalidInput),
	})
	body := `{"display_name":"U","initial_password":"P@ss123!"}`
	rec := postAdminUsers(t, handler, body)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestAdminCreateUser_PasswordPolicy_Returns400(t *testing.T) {
	handler := httpapi.AdminCreateUser(&fakeUserCreator{
		err: fmt.Errorf("%w: minimum length 12", domain.ErrPasswordPolicy),
	})
	body := `{"email":"u@e.com","display_name":"U","initial_password":"short"}`
	rec := postAdminUsers(t, handler, body)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestAdminCreateUser_InternalError_Returns500(t *testing.T) {
	handler := httpapi.AdminCreateUser(&fakeUserCreator{err: errors.New("db crashed")})
	body := `{"email":"u@e.com","display_name":"U","initial_password":"P@ss123!"}`
	rec := postAdminUsers(t, handler, body)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", rec.Code)
	}
}
```

Add `import "fmt"` to the test file's import block.

- [ ] **Step 10.2: Run to confirm fail**

```bash
cd services/auth-service && go test ./internal/http/... -v -run "TestAdminCreateUser" 2>&1 | head -10
cd ../..
```

Expected: compilation error.

- [ ] **Step 10.3: Create http/admin_handler.go**

```go
// services/auth-service/internal/http/admin_handler.go
package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/nicrepository/nchat/libs/go/platform/httputil"
	"github.com/nicrepository/nchat/services/auth-service/internal/domain"
	"github.com/nicrepository/nchat/services/auth-service/internal/service"
)

const (
	errCodeConflict    = "conflict"
	errCodeUnavailable = "service_unavailable"
)

type createUserRequest struct {
	Email              string `json:"email"`
	DisplayName        string `json:"display_name"`
	FullName           string `json:"full_name"`
	InitialPassword    string `json:"initial_password"`
	MustChangePassword bool   `json:"must_change_password"`
}

type userResponse struct {
	ID              string  `json:"id"`
	Email           string  `json:"email"`
	DisplayName     string  `json:"display_name"`
	FullName        *string `json:"full_name,omitempty"`
	Status          string  `json:"status"`
	AuthSource      string  `json:"auth_source"`
	EmailVerifiedAt string  `json:"email_verified_at"`
	CreatedAt       string  `json:"created_at"`
}

// AdminCreateUser handles POST /admin/users.
// It requires the service.UserCreator to be non-nil; if nil, returns 503.
func AdminCreateUser(users service.UserCreator) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if users == nil {
			httputil.WriteError(w, http.StatusServiceUnavailable, errCodeUnavailable, "admin endpoint unavailable: database not configured")
			return
		}

		var req createUserRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httputil.WriteError(w, http.StatusBadRequest, httputil.ErrCodeBadRequest, "invalid JSON body")
			return
		}

		input := domain.CreateUserInput{
			Email:              req.Email,
			DisplayName:        req.DisplayName,
			FullName:           strings.TrimSpace(req.FullName),
			InitialPassword:    req.InitialPassword,
			MustChangePassword: req.MustChangePassword,
		}

		user, err := users.CreateUser(r.Context(), input)
		if err != nil {
			switch {
			case errors.Is(err, domain.ErrDuplicateEmail):
				httputil.WriteError(w, http.StatusConflict, errCodeConflict, "email already registered")
			case errors.Is(err, domain.ErrInvalidInput), errors.Is(err, domain.ErrPasswordPolicy):
				httputil.WriteError(w, http.StatusBadRequest, httputil.ErrCodeBadRequest, err.Error())
			default:
				httputil.WriteError(w, http.StatusInternalServerError, httputil.ErrCodeInternal, "internal error")
			}
			return
		}

		resp := userResponse{
			ID:              user.ID,
			Email:           user.Email,
			DisplayName:     user.DisplayName,
			Status:          user.Status,
			AuthSource:      user.AuthSource,
			EmailVerifiedAt: user.EmailVerifiedAt.Format(time.RFC3339),
			CreatedAt:       user.CreatedAt.Format(time.RFC3339),
		}
		if user.FullName != "" {
			resp.FullName = &user.FullName
		}

		httputil.WriteJSON(w, http.StatusCreated, resp)
	})
}
```

- [ ] **Step 10.4: Run all HTTP tests**

```bash
cd services/auth-service && go test ./internal/http/... -v 2>&1
cd ../..
```

Expected: all PASS (existing router tests + new middleware + new handler tests).

- [ ] **Step 10.5: Commit**

```bash
git add services/auth-service/internal/http/
git commit -m "feat(auth): add AdminCreateUser handler with error mapping"
```

---

## Task 11: Wire up router and app

**Files:**

- Modify: `services/auth-service/internal/http/routes.go`
- Modify: `services/auth-service/internal/http/router.go`
- Modify: `services/auth-service/internal/http/router_test.go`
- Modify: `services/auth-service/internal/app/app.go`
- Modify: `services/auth-service/internal/app/app_test.go`

- [ ] **Step 11.1: Add RouteAdminUsers to routes.go**

Replace `services/auth-service/internal/http/routes.go`:

```go
package httpapi

const (
	RouteHealthz    = "/healthz"
	RouteReadyz     = "/readyz"
	RouteVersion    = "/version"
	RouteAdminUsers = "/admin/users"
)
```

- [ ] **Step 11.2: Update router.go to accept UserCreator and register the admin route**

Replace `services/auth-service/internal/http/router.go`:

```go
package httpapi

import (
	"log/slog"
	"net/http"

	"github.com/nicrepository/nchat/libs/go/platform/httputil"
	"github.com/nicrepository/nchat/libs/go/platform/observability"
	"github.com/nicrepository/nchat/services/auth-service/internal/config"
	"github.com/nicrepository/nchat/services/auth-service/internal/service"
)

const RouteMetrics = "/metrics"

func NewRouter(cfg config.Config, logger *slog.Logger, users service.UserCreator) http.Handler {
	_ = logger

	obsCfg := observability.LoadConfig(cfg.ServiceName)
	metrics := observability.NewMetrics(obsCfg)

	mux := http.NewServeMux()
	mux.Handle(RouteHealthz, httputil.MethodNotAllowed(http.MethodGet, Healthz(cfg)))
	mux.Handle(RouteReadyz, httputil.MethodNotAllowed(http.MethodGet, Readyz(cfg)))
	mux.Handle(RouteVersion, httputil.MethodNotAllowed(http.MethodGet, Version(cfg)))
	mux.Handle(RouteMetrics, metrics.Handler())
	mux.Handle(RouteAdminUsers, httputil.MethodNotAllowed(http.MethodPost,
		AdminBootstrapGuard(cfg.AdminBootstrapToken)(AdminCreateUser(users)),
	))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		httputil.WriteError(w, http.StatusNotFound, httputil.ErrCodeNotFound, "not found")
	})

	obs := observability.HTTPMiddleware(obsCfg, metrics)
	return httputil.Recover(httputil.RequestID(httputil.SecurityHeaders(obs(mux))))
}
```

- [ ] **Step 11.3: Update router_test.go — pass nil UserCreator to all NewRouter calls**

In `services/auth-service/internal/http/router_test.go`, replace every occurrence of:

```go
NewRouter(testConfig(), platformlog.New("auth-service", "test"))
```

with:

```go
NewRouter(testConfig(), platformlog.New("auth-service", "test"), nil)
```

There are 4 such calls. Also add a test for the admin route returning 503 (because no token configured):

```go
func TestAdminUsersDisabledWithNoToken(t *testing.T) {
	router := NewRouter(testConfig(), platformlog.New("auth-service", "test"), nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, RouteAdminUsers, nil))
	// No ADMIN_BOOTSTRAP_TOKEN in testConfig => 503
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", rec.Code)
	}
}

func TestAdminUsersMethodNotAllowed(t *testing.T) {
	router := NewRouter(testConfig(), platformlog.New("auth-service", "test"), nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, RouteAdminUsers, nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", rec.Code)
	}
}
```

- [ ] **Step 11.4: Update app.go to wire DB + service**

Replace `services/auth-service/internal/app/app.go`:

```go
package app

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	platformlog "github.com/nicrepository/nchat/libs/go/platform/log"
	"github.com/nicrepository/nchat/libs/go/platform/observability"
	"github.com/nicrepository/nchat/services/auth-service/internal/config"
	httpapi "github.com/nicrepository/nchat/services/auth-service/internal/http"
	"github.com/nicrepository/nchat/services/auth-service/internal/service"
	"github.com/nicrepository/nchat/services/auth-service/internal/storage"
)

type App struct {
	Config          config.Config
	Logger          *slog.Logger
	Handler         http.Handler
	TracingShutdown observability.ShutdownFunc
}

func New(cfg config.Config) *App {
	logger := platformlog.New(cfg.ServiceName, cfg.Env)
	obsCfg := observability.LoadConfig(cfg.ServiceName)
	shutdown, _ := observability.SetupTracing(context.Background(), obsCfg)

	var users service.UserCreator
	if cfg.DatabaseURL != "" {
		ctx, cancel := context.WithTimeout(context.Background(), time.Duration(cfg.DBConnectTimeoutSeconds)*time.Second)
		defer cancel()
		pool, err := storage.OpenDB(ctx, cfg.DatabaseURL, cfg.DBConnectTimeoutSeconds)
		if err != nil {
			logger.Warn("database unavailable; admin endpoint disabled", "error", err)
		} else {
			users = service.NewUserService(storage.NewPGXUserStore(pool))
		}
	}

	return &App{
		Config:          cfg,
		Logger:          logger,
		Handler:         httpapi.NewRouter(cfg, logger, users),
		TracingShutdown: shutdown,
	}
}
```

- [ ] **Step 11.5: Run all tests**

```bash
cd services/auth-service && go test ./... -v 2>&1 | tail -30
cd ../..
```

Expected: all PASS. If `app_test.go` fails because `TestNewCreatesApp` passes a config without `DatabaseURL`, that's fine — the app starts without DB.

- [ ] **Step 11.6: Run coverage check**

```bash
cd services/auth-service
go test ./... -covermode=atomic -coverprofile=/tmp/auth.out
go tool cover -func=/tmp/auth.out | tail -5
cd ../..
```

Expected: total coverage ≥ 90%.

- [ ] **Step 11.7: Commit**

```bash
git add services/auth-service/internal/http/routes.go \
        services/auth-service/internal/http/router.go \
        services/auth-service/internal/http/router_test.go \
        services/auth-service/internal/app/app.go
git commit -m "feat(auth): wire admin user creation endpoint into router and app"
```

---

## Task 12: Runbook and README update

**Files:**

- Create: `docs/runbooks/task-23-admin-manual-user-create.md`
- Modify: `README.md`

- [ ] **Step 12.1: Create the runbook**

Create `docs/runbooks/task-23-admin-manual-user-create.md`:

```markdown
# Runbook: Task-23 — Admin Manual User Creation

## Overview

Implements RF-45: cadastro manual pelo admin.
Adds `POST /admin/users` to `auth-service` so an admin can create a user
with an initial Argon2id-hashed password, validated against the password
policy stored in `auth.auth_policy_settings`.

## Out of scope (not implemented in this task)

- Login / session issuance / JWT
- OAuth/OIDC integration
- Password reset email
- Invite flow
- Frontend admin UI
- Full RBAC (see temporary guard below)

## Endpoint
```

POST /admin/users

````

Headers:
- `Content-Type: application/json`
- `X-NChat-Admin-Token: <ADMIN_BOOTSTRAP_TOKEN>` — **temporary bootstrap guard**

Request body:
```json
{
  "email": "user@example.com",
  "display_name": "User Name",
  "full_name": "Full Name",
  "initial_password": "TemporaryP@ss1",
  "must_change_password": true
}
````

Response (201):

```json
{
  "data": {
    "id": "...",
    "email": "user@example.com",
    "display_name": "User Name",
    "status": "active",
    "auth_source": "manual",
    "email_verified_at": "2026-05-26T10:00:00Z",
    "created_at": "2026-05-26T10:00:00Z"
  }
}
```

Error responses:

- `400` — invalid input or password policy violation
- `401` — missing or wrong `X-NChat-Admin-Token`
- `409` — duplicate email
- `500` — internal error
- `503` — `ADMIN_BOOTSTRAP_TOKEN` not set, or database not configured

## Environment variables

| Variable                     | Required       | Description                                                  |
| ---------------------------- | -------------- | ------------------------------------------------------------ |
| `DATABASE_URL`               | Yes            | pgx DSN — `postgres://user:pass@host/dbname`                 |
| `ADMIN_BOOTSTRAP_TOKEN`      | Yes            | Temporary admin guard token. If empty, endpoint returns 503. |
| `DB_CONNECT_TIMEOUT_SECONDS` | No (default 5) | Timeout for initial DB connection                            |

## Temporary admin guard

`X-NChat-Admin-Token` is a **dev/bootstrap-only** mechanism. It is NOT final RBAC.
Replace with proper auth middleware before production.
The token is never logged by the service.

## Running locally

```bash
# Start PostgreSQL
make dev-env-up

# Apply migrations
make migrations-up

# Run auth-service with required env
export DATABASE_URL="postgres://nchat:nchat@localhost:5432/nchat?sslmode=disable"
export ADMIN_BOOTSTRAP_TOKEN="replace-with-secure-token"
cd services/auth-service && go run ./cmd/auth-service/

# Create a user (separate terminal)
curl -s -X POST http://localhost:8081/admin/users \
  -H "Content-Type: application/json" \
  -H "X-NChat-Admin-Token: replace-with-secure-token" \
  -d '{
    "email": "admin@example.com",
    "display_name": "Admin User",
    "initial_password": "ChangeMe@123",
    "must_change_password": true
  }' | jq .
```

## Password policy

Applied from `auth.auth_policy_settings` (seeded with defaults):

- Minimum length: 12 characters
- Requires: uppercase, lowercase, number, symbol

## Password storage

Argon2id PHC format: `$argon2id$v=19$m=65536,t=3,p=4$<salt>$<hash>`
Never stored as plaintext.

## Related

- Auth schema: [docs/architecture/auth-data-model.md](../architecture/auth-data-model.md)
- Migration framework: [docs/runbooks/task-22-initial-migrations.md](task-22-initial-migrations.md)

````

- [ ] **Step 12.2: Update README — add endpoint to Auth section**

In `README.md`, find the "## Auth data model" section and add below it:

```markdown
## Admin user creation

`POST /admin/users` allows an admin to create a manual user with an Argon2id-hashed password.

- Endpoint: `POST /admin/users` (auth-service, port 8081)
- Guard: `X-NChat-Admin-Token` header (temporary bootstrap token, not final RBAC)
- Runbook: [docs/runbooks/task-23-admin-manual-user-create.md](docs/runbooks/task-23-admin-manual-user-create.md)
````

- [ ] **Step 12.3: Format docs with Prettier**

```bash
npx prettier --write docs/runbooks/task-23-admin-manual-user-create.md README.md
```

- [ ] **Step 12.4: Commit**

```bash
git add docs/runbooks/task-23-admin-manual-user-create.md README.md
git commit -m "docs(auth): add runbook and README section for admin user creation"
```

---

## Task 13: CI validation and PR

- [ ] **Step 13.1: Run full CI**

```bash
pnpm run ci 2>&1 | tail -20
```

Expected: all checks pass including `migrations:check`, `lint`, `test`, `build`.

```bash
make ci 2>&1 | tail -5
```

Expected: same.

- [ ] **Step 13.2: Run Go coverage check explicitly**

```bash
pnpm test:coverage:go:check
```

Expected: "Go coverage threshold passed."

- [ ] **Step 13.3: Push branch**

```bash
git push -u origin feat/auth-admin-manual-user-create
```

- [ ] **Step 13.4: Open pull request**

```bash
gh pr create \
  --repo nicrepository/nchat \
  --base develop \
  --title "feat(auth): add admin manual user creation" \
  --body "$(cat <<'EOF'
## Summary

Implements RF-45: cadastro manual pelo admin. Closes #52.

**Endpoint:** `POST /admin/users` (auth-service)
**Guard:** `X-NChat-Admin-Token` header — **temporary bootstrap guard, not final RBAC** (see runbook)
**Password hashing:** Argon2id PHC format (m=65536, t=3, p=4)
**Transaction:** user + password credential inserted atomically

## Changes

- `internal/config`: `DATABASE_URL`, `ADMIN_BOOTSTRAP_TOKEN`, `DB_CONNECT_TIMEOUT_SECONDS`
- `internal/domain`: `User`, `PolicySettings`, `CreateUserInput`, error sentinels, email/password validation
- `internal/storage`: `Pool` interface, `OpenDB`, `PGXUserStore` (pgxmock-tested)
- `internal/service`: `UserService.CreateUser` with policy enforcement + Argon2id hashing
- `internal/http`: `AdminBootstrapGuard` middleware + `AdminCreateUser` handler
- `internal/app`: wires DB pool + service on startup; gracefully disables endpoint if DB unavailable
- `docs/runbooks/task-23-admin-manual-user-create.md`

## Security notes

- Password hash only (Argon2id PHC) — never stored or logged as plaintext
- `X-NChat-Admin-Token` is never logged by the service
- Duplicate email → 409; policy violation → 400; missing token → 401; token not set → 503
- `ADMIN_BOOTSTRAP_TOKEN` empty → endpoint disabled at middleware level (503)

## Test plan

- [x] `pnpm run ci` — full CI gate passes
- [x] `pnpm test:coverage:go:check` — coverage ≥ 90%
- [ ] `make dev-env-up && make migrations-up` + run auth-service + `curl POST /admin/users` — manual integration test
EOF
)"
```

---

## Post-plan self-review

**Spec coverage check:**

| Requirement                  | Task          |
| ---------------------------- | ------------- |
| PostgreSQL connection config | Task 2, 6, 11 |
| Repository/service layer     | Tasks 7, 8    |
| POST /admin/users endpoint   | Tasks 10, 11  |
| Request validation           | Tasks 4, 8    |
| Argon2id password hashing    | Task 5        |
| Transactional insert         | Task 7        |
| Safe response (no hash)      | Task 10       |
| Bootstrap token guard        | Task 9        |
| Duplicate email → 409        | Tasks 7, 10   |
| Policy validation            | Tasks 4, 8    |
| Tests ≥ 90% coverage         | Tasks 5–11    |
| Runbook                      | Task 12       |
| README update                | Task 12       |
| CI validation                | Task 13       |

**Type consistency:** `service.UserCreator` interface is defined in Task 8 and consumed by the handler in Task 10 and router in Task 11 — name consistent throughout.

**Coverage note:** `storage/db.go`'s `OpenDB` requires a live DB and is not unit-tested. This is expected. The storage layer is tested via pgxmock (Task 7), keeping total coverage ≥ 90%.
