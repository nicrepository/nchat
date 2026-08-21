package storage_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	pgxmock "github.com/pashagolub/pgxmock/v2"

	"github.com/nicrepository/nchat/services/auth-service/internal/domain"
	"github.com/nicrepository/nchat/services/auth-service/internal/storage"
)

// expectDefaultWorkspaceEnrollment is the membership insert every successful
// JIT provisioning must issue, between the user insert and the login attempt.
func expectDefaultWorkspaceEnrollment(mock pgxmock.PgxPoolIface) *pgxmock.ExpectedQuery {
	return mock.ExpectQuery(`INSERT INTO chat\.workspace_members`).
		WithArgs("user-id").
		WillReturnRows(pgxmock.NewRows([]string{"workspace_id"}).AddRow("workspace-id"))
}

// expectProvisioningUpTo replays the statements a first login runs before the
// membership insert, so each test below only has to describe how the
// enrollment itself fails.
func expectProvisioningUpToEnrollment(mock pgxmock.PgxPoolIface) {
	mock.ExpectBegin()
	expectOIDCPolicyQuery(mock)
	mock.ExpectQuery(`SELECT id, email::text, display_name, status, deleted_at`).
		WithArgs("keycloak", "subject-1").
		WillReturnError(pgx.ErrNoRows)
	mock.ExpectQuery(`SELECT id\s+FROM auth\.users`).
		WithArgs("new@example.com").
		WillReturnError(pgx.ErrNoRows)
	mock.ExpectQuery(`INSERT INTO auth\.users`).
		WithArgs("new@example.com", "New User", "", "", "keycloak", "subject-1").
		WillReturnRows(pgxmock.NewRows([]string{"id", "email", "display_name"}).AddRow("user-id", "new@example.com", "New User"))
}

func provisioningInput() domain.OIDCSessionInput {
	return domain.OIDCSessionInput{
		Provider:         "keycloak",
		Subject:          "subject-1",
		Email:            "new@example.com",
		DisplayName:      "New User",
		RefreshTokenHash: "refresh-hash",
		RefreshExpiresAt: time.Now().Add(time.Hour),
		AutoProvision:    true,
	}
}

// A deployment with no active default workspace must not produce a user who
// can authenticate but cannot open a channel. The login fails and the
// transaction rolls back before any session or exchange code is written — the
// expectations stop at the rollback, so an extra statement fails the test.
func TestPGXOIDCStore_MissingDefaultWorkspaceAbortsProvisioning(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	expectProvisioningUpToEnrollment(mock)
	mock.ExpectQuery(`INSERT INTO chat\.workspace_members`).
		WithArgs("user-id").
		WillReturnError(pgx.ErrNoRows)
	mock.ExpectRollback()

	store := storage.NewPGXOIDCStore(mock)
	_, err = store.CreateOIDCSessionAndExchange(context.Background(), provisioningInput(),
		func(domain.Session, domain.LoginUser) (domain.OIDCExchangeInput, error) {
			t.Fatal("the exchange must never be built when provisioning fails")
			return domain.OIDCExchangeInput{}, nil
		})
	if err == nil {
		t.Fatal("expected the missing default workspace to fail the login")
	}
	if !strings.Contains(err.Error(), "default workspace") {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// Any other failure of the membership insert is equally fatal to the login:
// the account, the session and the exchange code go away together.
func TestPGXOIDCStore_EnrollmentErrorAbortsProvisioning(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	insertErr := errors.New("permission denied for table workspace_members")
	expectProvisioningUpToEnrollment(mock)
	mock.ExpectQuery(`INSERT INTO chat\.workspace_members`).
		WithArgs("user-id").
		WillReturnError(insertErr)
	mock.ExpectRollback()

	store := storage.NewPGXOIDCStore(mock)
	_, err = store.CreateOIDCSessionAndExchange(context.Background(), provisioningInput(),
		func(domain.Session, domain.LoginUser) (domain.OIDCExchangeInput, error) {
			t.Fatal("the exchange must never be built when provisioning fails")
			return domain.OIDCExchangeInput{}, nil
		})
	if !errors.Is(err, insertErr) {
		t.Fatalf("expected the insert error to reach the caller, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}
