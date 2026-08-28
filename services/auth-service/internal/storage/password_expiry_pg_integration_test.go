package storage_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nicrepository/nchat/services/auth-service/internal/domain"
	"github.com/nicrepository/nchat/services/auth-service/internal/service"
	"github.com/nicrepository/nchat/services/auth-service/internal/storage"
)

// RF-47 password expiry, against the real schema.
//
// A mock can prove the store sends the right statement. Only a database can
// prove that the column the Admin Console writes is the column the login path
// reads — which is the whole claim these specs exist to check: changing
// `auth.password.expiration_days` from the console changes who can log in.
//
// Gated on AUTH_TEST_DATABASE_URL, like the rest of this package's PostgreSQL
// suite, and skipped when it is unset.
//
//	AUTH_TEST_DATABASE_URL=postgresql://nchat@localhost:5432/nchat_test \
//	  go test ./internal/storage/... -run PostgreSQL

// setPasswordExpirationDays writes the policy exactly as the Admin API does:
// the same single row, the same column, NULL for "never expires".
func setPasswordExpirationDays(t *testing.T, pool *pgxpool.Pool, days any) {
	t.Helper()
	if _, err := pool.Exec(context.Background(),
		`UPDATE auth.auth_policy_settings SET password_expiration_days = $1 WHERE id = 1`, days); err != nil {
		t.Fatalf("set password_expiration_days: %v", err)
	}
}

// givePassword creates the credential and backdates it, so the age under test
// is a stored fact rather than something the spec has to wait for.
func givePassword(t *testing.T, pool *pgxpool.Pool, userID, password string, age time.Duration) {
	t.Helper()
	hash, err := service.HashPassword(password)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO auth.user_password_credentials (user_id, password_hash, password_changed_at)
		VALUES ($1::uuid, $2, now() - $3::interval)`, userID, hash, age.String()); err != nil {
		t.Fatalf("insert credential: %v", err)
	}
}

// loginWith performs a real login through the store under test.
//
// The refresh hash is unique per call because the column is: a spec that logs
// in twice is testing the policy, not the uniqueness constraint.
func loginWith(pool *pgxpool.Pool, email, password string) (domain.CreatedLoginSession, error) {
	store := storage.NewPGXLoginStore(pool, service.VerifyPassword, service.RunDummyPasswordVerification)
	return store.CreateLoginSession(context.Background(), domain.CreateSessionInput{
		Email:            email,
		Password:         password,
		IPAddress:        "203.0.113.10",
		UserAgent:        "integration",
		RefreshTokenHash: uuid.NewString(),
		RefreshExpiresAt: time.Now().Add(30 * 24 * time.Hour),
	})
}

func countSessions(t *testing.T, pool *pgxpool.Pool, userID string) int {
	t.Helper()
	var sessions int
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM auth.user_sessions WHERE user_id = $1::uuid`, userID).Scan(&sessions); err != nil {
		t.Fatalf("count sessions: %v", err)
	}
	return sessions
}

// The claim the Admin Console makes when it says this setting is applied at
// runtime: the same password, the same age, two different policies, two
// different outcomes — with no restart in between.
func TestPGXLoginStore_PasswordExpiryFollowsThePolicyPostgreSQL(t *testing.T) {
	pool := connectAuthTestDB(t)
	applyAuthMigrations(t, pool)

	userID := insertActiveUser(t, pool, "expiry@example.test")
	givePassword(t, pool, userID, "Correct-Horse-1!", 100*24*time.Hour)

	// The platform default: passwords do not expire, so a 100-day-old one works.
	setPasswordExpirationDays(t, pool, nil)
	session, err := loginWith(pool, "expiry@example.test", "Correct-Horse-1!")
	if err != nil {
		t.Fatalf("login with no expiry policy: %v", err)
	}
	if session.Session.ID == "" {
		t.Fatal("expected a session")
	}
	if got := countSessions(t, pool, userID); got != 1 {
		t.Fatalf("expected exactly one session, got %d", got)
	}

	// An administrator tightens the policy through the Admin Console. Nothing
	// is restarted and no cache is invalidated.
	setPasswordExpirationDays(t, pool, 90)

	_, err = loginWith(pool, "expiry@example.test", "Correct-Horse-1!")
	if !errors.Is(err, domain.ErrPasswordExpired) {
		t.Fatalf("expected the new policy to refuse the old password, got %v", err)
	}
	if got := countSessions(t, pool, userID); got != 1 {
		t.Fatalf("an expired password must not create a session, got %d", got)
	}

	// Loosening it again is equally immediate.
	setPasswordExpirationDays(t, pool, 365)
	if _, err := loginWith(pool, "expiry@example.test", "Correct-Horse-1!"); err != nil {
		t.Fatalf("expected the relaxed policy to admit the password again: %v", err)
	}
	if got := countSessions(t, pool, userID); got != 2 {
		t.Fatalf("expected a second session, got %d", got)
	}
}

// A password inside the window authenticates, and one outside it does not. Both
// under the same policy, so the difference is the age and nothing else.
func TestPGXLoginStore_PasswordExpiryBoundaryPostgreSQL(t *testing.T) {
	pool := connectAuthTestDB(t)
	applyAuthMigrations(t, pool)
	setPasswordExpirationDays(t, pool, 90)

	fresh := insertActiveUser(t, pool, "fresh@example.test")
	givePassword(t, pool, fresh, "Correct-Horse-1!", 89*24*time.Hour)
	stale := insertActiveUser(t, pool, "stale@example.test")
	givePassword(t, pool, stale, "Correct-Horse-1!", 91*24*time.Hour)

	if _, err := loginWith(pool, "fresh@example.test", "Correct-Horse-1!"); err != nil {
		t.Fatalf("a password inside the window must authenticate: %v", err)
	}

	_, err := loginWith(pool, "stale@example.test", "Correct-Horse-1!")
	if !errors.Is(err, domain.ErrPasswordExpired) {
		t.Fatalf("a password outside the window must be refused, got %v", err)
	}
	if got := countSessions(t, pool, stale); got != 0 {
		t.Fatalf("expected no session for the expired password, got %d", got)
	}
}

// The refusal is recorded under its own reason, which the lockout query does
// not count: presenting a correct password must never lock the account.
func TestPGXLoginStore_ExpiredPasswordDoesNotLockTheAccountPostgreSQL(t *testing.T) {
	pool := connectAuthTestDB(t)
	applyAuthMigrations(t, pool)
	setPasswordExpirationDays(t, pool, 30)

	userID := insertActiveUser(t, pool, "notlocked@example.test")
	givePassword(t, pool, userID, "Correct-Horse-1!", 60*24*time.Hour)

	// More attempts than failed_login_limit, which defaults to five.
	for attempt := 0; attempt < 6; attempt++ {
		if _, err := loginWith(pool, "notlocked@example.test", "Correct-Horse-1!"); !errors.Is(err, domain.ErrPasswordExpired) {
			t.Fatalf("attempt %d: expected ErrPasswordExpired, got %v", attempt, err)
		}
	}

	var reasons int
	if err := pool.QueryRow(context.Background(), `
		SELECT count(*) FROM auth.login_attempts
		WHERE user_id = $1::uuid AND failure_reason = 'password_expired'`, userID).Scan(&reasons); err != nil {
		t.Fatalf("count attempts: %v", err)
	}
	if reasons != 6 {
		t.Fatalf("expected every refusal in the trail, got %d", reasons)
	}

	// Relaxing the policy lets the same password straight back in: the account
	// was refused, never locked.
	setPasswordExpirationDays(t, pool, nil)
	if _, err := loginWith(pool, "notlocked@example.test", "Correct-Horse-1!"); err != nil {
		t.Fatalf("the account must not have been locked by expiry refusals: %v", err)
	}
}

// Resetting the password restarts its age. Without this, an expiry policy would
// be a one-way door: the reset would succeed and the login would still refuse.
func TestPGXPasswordResetStore_ResetRestartsThePasswordAgePostgreSQL(t *testing.T) {
	pool := connectAuthTestDB(t)
	applyAuthMigrations(t, pool)
	setPasswordExpirationDays(t, pool, 30)
	ctx := context.Background()

	userID := insertActiveUser(t, pool, "reset@example.test")
	givePassword(t, pool, userID, "Old-Horse-1!", 60*24*time.Hour)

	if _, err := loginWith(pool, "reset@example.test", "Old-Horse-1!"); !errors.Is(err, domain.ErrPasswordExpired) {
		t.Fatalf("expected the aged password to be refused first, got %v", err)
	}

	newHash, err := service.HashPassword("Brand-New-Horse-1!")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE auth.user_password_credentials
		SET password_hash = $2, password_changed_at = now(), must_change_password = false
		WHERE user_id = $1::uuid`, userID, newHash); err != nil {
		t.Fatalf("reset password: %v", err)
	}

	if _, err := loginWith(pool, "reset@example.test", "Brand-New-Horse-1!"); err != nil {
		t.Fatalf("a freshly set password must authenticate under the same policy: %v", err)
	}
}
