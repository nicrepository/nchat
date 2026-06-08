package storage_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	pgxmock "github.com/pashagolub/pgxmock/v2"

	"github.com/nicrepository/nchat/services/auth-service/internal/domain"
	"github.com/nicrepository/nchat/services/auth-service/internal/storage"
)

func TestPGXOIDCStore_CreateAuthRequestStoresHashedState(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	expiresAt := time.Now().Add(10 * time.Minute)
	mock.ExpectExec(`INSERT INTO auth\.oidc_auth_requests`).
		WithArgs("auth-id", "keycloak", "state-hash", "nonce-hash", "encrypted-verifier", nil, expiresAt).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))

	store := storage.NewPGXOIDCStore(mock)
	err = store.CreateAuthRequest(context.Background(), domain.OIDCLoginRequest{
		ID:                    "auth-id",
		Provider:              "keycloak",
		StateHash:             "state-hash",
		NonceHash:             "nonce-hash",
		PKCEVerifierEncrypted: "encrypted-verifier",
		ExpiresAt:             expiresAt,
	})
	if err != nil {
		t.Fatalf("CreateAuthRequest: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXOIDCStore_ConsumeAuthRequestRejectsMissingExpiredOrReplayedState(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectQuery(`UPDATE auth\.oidc_auth_requests`).
		WithArgs("keycloak", "state-hash").
		WillReturnError(pgx.ErrNoRows)

	store := storage.NewPGXOIDCStore(mock)
	_, err = store.ConsumeAuthRequest(context.Background(), "keycloak", "state-hash")
	if !errors.Is(err, domain.ErrInvalidToken) {
		t.Fatalf("expected ErrInvalidToken, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXOIDCStore_CreateOIDCSessionAndExchangeDoesNotSilentlyLinkManualEmail(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectBegin()
	expectOIDCPolicyQuery(mock)
	mock.ExpectQuery(`SELECT id, email::text, display_name, status, deleted_at`).
		WithArgs("keycloak", "subject-1").
		WillReturnError(pgx.ErrNoRows)
	mock.ExpectQuery(`SELECT id\s+FROM auth\.users`).
		WithArgs("manual@example.com").
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow("manual-user-id"))
	mock.ExpectRollback()

	store := storage.NewPGXOIDCStore(mock)
	buildCalled := false
	_, err = store.CreateOIDCSessionAndExchange(context.Background(), domain.OIDCSessionInput{
		Provider:         "keycloak",
		Subject:          "subject-1",
		Email:            "manual@example.com",
		DisplayName:      "Manual",
		RefreshTokenHash: "refresh-hash",
		RefreshExpiresAt: time.Now().Add(time.Hour),
		AutoProvision:    true,
	}, func(domain.Session, domain.LoginUser) (domain.OIDCExchangeInput, error) {
		buildCalled = true
		return domain.OIDCExchangeInput{}, nil
	})
	if !errors.Is(err, domain.ErrOIDCAccountConflict) {
		t.Fatalf("expected account conflict, got %v", err)
	}
	if buildCalled {
		t.Fatal("exchange builder must not run on account conflict")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXOIDCStore_ConsumeExchangeReturnsSafeUserAndEncryptedValues(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectQuery(`UPDATE auth\.oidc_exchange_codes`).
		WithArgs("keycloak", "code-hash").
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "provider", "access_value_encrypted", "refresh_value_encrypted", "bearer_scheme", "expires_in", "user_json",
		}).AddRow("exchange-id", "keycloak", "encrypted-access", "encrypted-refresh", "Bearer", 900, []byte(`{"id":"u1","email":"user@example.com","display_name":"User","must_change_password":false}`)))

	store := storage.NewPGXOIDCStore(mock)
	exchange, err := store.ConsumeExchange(context.Background(), "keycloak", "code-hash")
	if err != nil {
		t.Fatalf("ConsumeExchange: %v", err)
	}
	if exchange.AccessValueEncrypted != "encrypted-access" || exchange.RefreshValueEncrypted != "encrypted-refresh" {
		t.Fatalf("unexpected encrypted values: %+v", exchange)
	}
	if exchange.User.Email != "user@example.com" || exchange.User.MustChangePassword {
		t.Fatalf("unexpected user: %+v", exchange.User)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func expectOIDCPolicyQuery(mock pgxmock.PgxPoolIface) {
	mock.ExpectQuery(`SELECT min_password_length`).
		WillReturnRows(pgxmock.NewRows([]string{
			"min_password_length", "require_uppercase", "require_lowercase",
			"require_number", "require_symbol", "failed_login_limit",
			"failed_login_window_minutes", "failed_login_lockout_minutes",
			"session_idle_timeout_minutes", "max_devices_per_user",
			"password_reset_token_ttl_minutes", "invite_token_ttl_hours",
		}).AddRow(12, true, true, true, true, 5, 15, 15, 60, 5, 60, 72))
}

func TestPGXOIDCStore_CreateOIDCSessionAndExchangeAutoProvisionsUser(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	refreshExpiresAt := time.Now().Add(time.Hour)
	exchangeExpiresAt := time.Now().Add(2 * time.Minute)

	mock.ExpectBegin()
	expectOIDCPolicyQuery(mock)
	mock.ExpectQuery(`SELECT id, email::text, display_name, status, deleted_at`).
		WithArgs("keycloak", "subject-1").
		WillReturnError(pgx.ErrNoRows)
	mock.ExpectQuery(`SELECT id\s+FROM auth\.users`).
		WithArgs("new@example.com").
		WillReturnError(pgx.ErrNoRows)
	mock.ExpectQuery(`INSERT INTO auth\.users`).
		WithArgs("new@example.com", "New User", "keycloak", "subject-1").
		WillReturnRows(pgxmock.NewRows([]string{"id", "email", "display_name"}).AddRow("user-id", "new@example.com", "New User"))
	mock.ExpectExec(`INSERT INTO auth\.login_attempts`).
		WithArgs("user-id", "new@example.com", true, nil, nil, nil).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectQuery(`INSERT INTO auth\.user_sessions`).
		WithArgs("user-id", nil, "refresh-hash", nil, nil, pgxmock.AnyArg(), refreshExpiresAt).
		WillReturnRows(pgxmock.NewRows([]string{"id", "user_id"}).AddRow("session-id", "user-id"))
	mock.ExpectExec(`INSERT INTO auth\.refresh_token_history`).
		WithArgs("session-id", "refresh-hash").
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectExec(`UPDATE auth\.users SET last_login_at`).
		WithArgs("user-id").
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectExec(`INSERT INTO auth\.oidc_exchange_codes`).
		WithArgs("exchange-id", "keycloak", "code-hash", "encrypted-access", "encrypted-refresh", "Bearer", 900, pgxmock.AnyArg(), exchangeExpiresAt).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectCommit()
	mock.ExpectRollback()

	store := storage.NewPGXOIDCStore(mock)
	created, err := store.CreateOIDCSessionAndExchange(context.Background(), domain.OIDCSessionInput{
		Provider:         "keycloak",
		Subject:          "subject-1",
		Email:            "new@example.com",
		DisplayName:      "New User",
		RefreshTokenHash: "refresh-hash",
		RefreshExpiresAt: refreshExpiresAt,
		AutoProvision:    true,
	}, func(session domain.Session, user domain.LoginUser) (domain.OIDCExchangeInput, error) {
		if session.ID != "session-id" || user.ID != "user-id" {
			t.Fatalf("unexpected builder input session=%+v user=%+v", session, user)
		}
		return domain.OIDCExchangeInput{
			ID:                    "exchange-id",
			Provider:              "keycloak",
			CodeHash:              "code-hash",
			AccessValueEncrypted:  "encrypted-access",
			RefreshValueEncrypted: "encrypted-refresh",
			BearerScheme:          "Bearer",
			ExpiresIn:             900,
			User:                  user,
			ExpiresAt:             exchangeExpiresAt,
		}, nil
	})
	if err != nil {
		t.Fatalf("CreateOIDCSessionAndExchange: %v", err)
	}
	if created.Session.ID != "session-id" || created.User.Email != "new@example.com" {
		t.Fatalf("unexpected created session: %+v", created)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXOIDCStore_ConsumeAuthRequestReturnsStoredEncryptedVerifier(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectQuery(`UPDATE auth\.oidc_auth_requests`).
		WithArgs("keycloak", "state-hash").
		WillReturnRows(pgxmock.NewRows([]string{"id", "provider", "nonce_hash", "pkce_verifier_encrypted", "redirect_after"}).
			AddRow("auth-id", "keycloak", "nonce-hash", "encrypted-verifier", ""))

	store := storage.NewPGXOIDCStore(mock)
	req, err := store.ConsumeAuthRequest(context.Background(), "keycloak", "state-hash")
	if err != nil {
		t.Fatalf("ConsumeAuthRequest: %v", err)
	}
	if req.ID != "auth-id" || req.PKCEVerifierEncrypted != "encrypted-verifier" {
		t.Fatalf("unexpected request: %+v", req)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXOIDCStore_CreateOIDCSessionAndExchangeLogsInExistingLinkedUser(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	refreshExpiresAt := time.Now().Add(time.Hour)
	exchangeExpiresAt := time.Now().Add(2 * time.Minute)

	mock.ExpectBegin()
	expectOIDCPolicyQuery(mock)
	mock.ExpectQuery(`SELECT id, email::text, display_name, status, deleted_at`).
		WithArgs("keycloak", "subject-1").
		WillReturnRows(pgxmock.NewRows([]string{"id", "email", "display_name", "status", "deleted_at"}).
			AddRow("user-id", "linked@example.com", "Linked User", "active", nil))
	mock.ExpectExec(`INSERT INTO auth\.login_attempts`).
		WithArgs("user-id", "linked@example.com", true, nil, nil, nil).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectQuery(`INSERT INTO auth\.user_sessions`).
		WithArgs("user-id", nil, "refresh-hash", nil, nil, pgxmock.AnyArg(), refreshExpiresAt).
		WillReturnRows(pgxmock.NewRows([]string{"id", "user_id"}).AddRow("session-id", "user-id"))
	mock.ExpectExec(`INSERT INTO auth\.refresh_token_history`).
		WithArgs("session-id", "refresh-hash").
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectExec(`UPDATE auth\.users SET last_login_at`).
		WithArgs("user-id").
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectExec(`INSERT INTO auth\.oidc_exchange_codes`).
		WithArgs("exchange-id", "keycloak", "code-hash", "encrypted-access", "encrypted-refresh", "Bearer", 900, pgxmock.AnyArg(), exchangeExpiresAt).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectCommit()
	mock.ExpectRollback()

	store := storage.NewPGXOIDCStore(mock)
	created, err := store.CreateOIDCSessionAndExchange(context.Background(), domain.OIDCSessionInput{
		Provider:         "keycloak",
		Subject:          "subject-1",
		Email:            "linked@example.com",
		RefreshTokenHash: "refresh-hash",
		RefreshExpiresAt: refreshExpiresAt,
		AutoProvision:    true,
	}, func(_ domain.Session, user domain.LoginUser) (domain.OIDCExchangeInput, error) {
		return domain.OIDCExchangeInput{ID: "exchange-id", Provider: "keycloak", CodeHash: "code-hash", AccessValueEncrypted: "encrypted-access", RefreshValueEncrypted: "encrypted-refresh", BearerScheme: "Bearer", ExpiresIn: 900, User: user, ExpiresAt: exchangeExpiresAt}, nil
	})
	if err != nil {
		t.Fatalf("CreateOIDCSessionAndExchange: %v", err)
	}
	if created.User.DisplayName != "Linked User" {
		t.Fatalf("unexpected linked user: %+v", created.User)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXOIDCStore_CreateOIDCSessionAndExchangeRejectsDisabledProvisioning(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectBegin()
	expectOIDCPolicyQuery(mock)
	mock.ExpectQuery(`SELECT id, email::text, display_name, status, deleted_at`).
		WithArgs("keycloak", "subject-1").
		WillReturnError(pgx.ErrNoRows)
	mock.ExpectQuery(`SELECT id\s+FROM auth\.users`).
		WithArgs("new@example.com").
		WillReturnError(pgx.ErrNoRows)
	mock.ExpectRollback()

	store := storage.NewPGXOIDCStore(mock)
	_, err = store.CreateOIDCSessionAndExchange(context.Background(), domain.OIDCSessionInput{Provider: "keycloak", Subject: "subject-1", Email: "new@example.com", AutoProvision: false}, func(domain.Session, domain.LoginUser) (domain.OIDCExchangeInput, error) {
		t.Fatal("builder must not run")
		return domain.OIDCExchangeInput{}, nil
	})
	if !errors.Is(err, domain.ErrOIDCProvisioningDisabled) {
		t.Fatalf("expected provisioning disabled, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXOIDCStore_ConsumeExchangeRejectsMissingExpiredOrReplayedCode(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectQuery(`UPDATE auth\.oidc_exchange_codes`).
		WithArgs("keycloak", "code-hash").
		WillReturnError(pgx.ErrNoRows)

	store := storage.NewPGXOIDCStore(mock)
	_, err = store.ConsumeExchange(context.Background(), "keycloak", "code-hash")
	if !errors.Is(err, domain.ErrInvalidToken) {
		t.Fatalf("expected ErrInvalidToken, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXOIDCStore_CreateAuthRequestReturnsInsertError(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()
	mock.ExpectExec(`INSERT INTO auth\.oidc_auth_requests`).
		WithArgs("", "", "", "", "", nil, time.Time{}).
		WillReturnError(errors.New("insert failed"))
	store := storage.NewPGXOIDCStore(mock)
	if err := store.CreateAuthRequest(context.Background(), domain.OIDCLoginRequest{}); err == nil {
		t.Fatal("expected insert error")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXOIDCStore_CreateOIDCSessionAndExchangeBeginError(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()
	mock.ExpectBegin().WillReturnError(errors.New("begin failed"))
	store := storage.NewPGXOIDCStore(mock)
	_, err = store.CreateOIDCSessionAndExchange(context.Background(), domain.OIDCSessionInput{}, func(domain.Session, domain.LoginUser) (domain.OIDCExchangeInput, error) {
		return domain.OIDCExchangeInput{}, nil
	})
	if err == nil {
		t.Fatal("expected begin error")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXOIDCStore_CreateOIDCSessionAndExchangeBuilderError(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()
	refreshExpiresAt := time.Now().Add(time.Hour)
	mock.ExpectBegin()
	expectOIDCPolicyQuery(mock)
	mock.ExpectQuery(`SELECT id, email::text, display_name, status, deleted_at`).
		WithArgs("keycloak", "subject-1").
		WillReturnRows(pgxmock.NewRows([]string{"id", "email", "display_name", "status", "deleted_at"}).AddRow("user-id", "linked@example.com", "Linked User", "active", nil))
	mock.ExpectExec(`INSERT INTO auth\.login_attempts`).
		WithArgs("user-id", "linked@example.com", true, nil, nil, nil).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectQuery(`INSERT INTO auth\.user_sessions`).
		WithArgs("user-id", nil, "refresh-hash", nil, nil, pgxmock.AnyArg(), refreshExpiresAt).
		WillReturnRows(pgxmock.NewRows([]string{"id", "user_id"}).AddRow("session-id", "user-id"))
	mock.ExpectExec(`INSERT INTO auth\.refresh_token_history`).
		WithArgs("session-id", "refresh-hash").
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectExec(`UPDATE auth\.users SET last_login_at`).
		WithArgs("user-id").
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectRollback()
	store := storage.NewPGXOIDCStore(mock)
	_, err = store.CreateOIDCSessionAndExchange(context.Background(), domain.OIDCSessionInput{Provider: "keycloak", Subject: "subject-1", Email: "linked@example.com", RefreshTokenHash: "refresh-hash", RefreshExpiresAt: refreshExpiresAt, AutoProvision: true}, func(domain.Session, domain.LoginUser) (domain.OIDCExchangeInput, error) {
		return domain.OIDCExchangeInput{}, errors.New("builder failed")
	})
	if err == nil {
		t.Fatal("expected builder error")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXOIDCStore_ConsumeExchangeRejectsMalformedUserJSON(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()
	mock.ExpectQuery(`UPDATE auth\.oidc_exchange_codes`).
		WithArgs("keycloak", "code-hash").
		WillReturnRows(pgxmock.NewRows([]string{"id", "provider", "access_value_encrypted", "refresh_value_encrypted", "bearer_scheme", "expires_in", "user_json"}).
			AddRow("exchange-id", "keycloak", "encrypted-access", "encrypted-refresh", "Bearer", 900, []byte(`not-json`)))
	store := storage.NewPGXOIDCStore(mock)
	_, err = store.ConsumeExchange(context.Background(), "keycloak", "code-hash")
	if err == nil {
		t.Fatal("expected malformed json error")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXOIDCStore_CreateOIDCSessionAndExchangeRejectsInactiveLinkedUser(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectBegin()
	expectOIDCPolicyQuery(mock)
	mock.ExpectQuery(`SELECT id, email::text, display_name, status, deleted_at`).
		WithArgs("keycloak", "subject-1").
		WillReturnRows(pgxmock.NewRows([]string{"id", "email", "display_name", "status", "deleted_at"}).
			AddRow("user-id", "linked@example.com", "Linked User", "suspended", nil))
	mock.ExpectRollback()

	store := storage.NewPGXOIDCStore(mock)
	_, err = store.CreateOIDCSessionAndExchange(context.Background(), domain.OIDCSessionInput{Provider: "keycloak", Subject: "subject-1", Email: "linked@example.com", AutoProvision: true}, func(domain.Session, domain.LoginUser) (domain.OIDCExchangeInput, error) {
		t.Fatal("builder must not run for inactive linked user")
		return domain.OIDCExchangeInput{}, nil
	})
	if !errors.Is(err, domain.ErrInvalidCredentials) {
		t.Fatalf("expected invalid credentials, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXOIDCStore_CreateOIDCSessionAndExchangeReturnsLinkedUserLookupError(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectBegin()
	expectOIDCPolicyQuery(mock)
	mock.ExpectQuery(`SELECT id, email::text, display_name, status, deleted_at`).
		WithArgs("keycloak", "subject-1").
		WillReturnError(errors.New("lookup failed"))
	mock.ExpectRollback()

	store := storage.NewPGXOIDCStore(mock)
	_, err = store.CreateOIDCSessionAndExchange(context.Background(), domain.OIDCSessionInput{Provider: "keycloak", Subject: "subject-1", Email: "linked@example.com", AutoProvision: true}, func(domain.Session, domain.LoginUser) (domain.OIDCExchangeInput, error) {
		return domain.OIDCExchangeInput{}, nil
	})
	if err == nil {
		t.Fatal("expected linked user lookup error")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXOIDCStore_CreateOIDCSessionAndExchangeReturnsEmailLookupError(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectBegin()
	expectOIDCPolicyQuery(mock)
	mock.ExpectQuery(`SELECT id, email::text, display_name, status, deleted_at`).
		WithArgs("keycloak", "subject-1").
		WillReturnError(pgx.ErrNoRows)
	mock.ExpectQuery(`SELECT id\s+FROM auth\.users`).
		WithArgs("new@example.com").
		WillReturnError(errors.New("email lookup failed"))
	mock.ExpectRollback()

	store := storage.NewPGXOIDCStore(mock)
	_, err = store.CreateOIDCSessionAndExchange(context.Background(), domain.OIDCSessionInput{Provider: "keycloak", Subject: "subject-1", Email: "new@example.com", AutoProvision: true}, func(domain.Session, domain.LoginUser) (domain.OIDCExchangeInput, error) {
		return domain.OIDCExchangeInput{}, nil
	})
	if err == nil {
		t.Fatal("expected email lookup error")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXOIDCStore_CreateOIDCSessionAndExchangeReturnsInsertUserError(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectBegin()
	expectOIDCPolicyQuery(mock)
	mock.ExpectQuery(`SELECT id, email::text, display_name, status, deleted_at`).
		WithArgs("keycloak", "subject-1").
		WillReturnError(pgx.ErrNoRows)
	mock.ExpectQuery(`SELECT id\s+FROM auth\.users`).
		WithArgs("new@example.com").
		WillReturnError(pgx.ErrNoRows)
	mock.ExpectQuery(`INSERT INTO auth\.users`).
		WithArgs("new@example.com", "New User", "keycloak", "subject-1").
		WillReturnError(errors.New("insert user failed"))
	mock.ExpectRollback()

	store := storage.NewPGXOIDCStore(mock)
	_, err = store.CreateOIDCSessionAndExchange(context.Background(), domain.OIDCSessionInput{Provider: "keycloak", Subject: "subject-1", Email: "new@example.com", DisplayName: "New User", AutoProvision: true}, func(domain.Session, domain.LoginUser) (domain.OIDCExchangeInput, error) {
		return domain.OIDCExchangeInput{}, nil
	})
	if err == nil {
		t.Fatal("expected insert user error")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXOIDCStore_CreateOIDCSessionAndExchangeReturnsExchangeInsertError(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()
	refreshExpiresAt := time.Now().Add(time.Hour)
	exchangeExpiresAt := time.Now().Add(2 * time.Minute)

	mock.ExpectBegin()
	expectOIDCPolicyQuery(mock)
	mock.ExpectQuery(`SELECT id, email::text, display_name, status, deleted_at`).
		WithArgs("keycloak", "subject-1").
		WillReturnRows(pgxmock.NewRows([]string{"id", "email", "display_name", "status", "deleted_at"}).AddRow("user-id", "linked@example.com", "Linked User", "active", nil))
	mock.ExpectExec(`INSERT INTO auth\.login_attempts`).
		WithArgs("user-id", "linked@example.com", true, nil, nil, nil).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectQuery(`INSERT INTO auth\.user_sessions`).
		WithArgs("user-id", nil, "refresh-hash", nil, nil, pgxmock.AnyArg(), refreshExpiresAt).
		WillReturnRows(pgxmock.NewRows([]string{"id", "user_id"}).AddRow("session-id", "user-id"))
	mock.ExpectExec(`INSERT INTO auth\.refresh_token_history`).
		WithArgs("session-id", "refresh-hash").
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectExec(`UPDATE auth\.users SET last_login_at`).
		WithArgs("user-id").
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectExec(`INSERT INTO auth\.oidc_exchange_codes`).
		WithArgs("exchange-id", "keycloak", "code-hash", "encrypted-access", "encrypted-refresh", "Bearer", 900, pgxmock.AnyArg(), exchangeExpiresAt).
		WillReturnError(errors.New("insert exchange failed"))
	mock.ExpectRollback()

	store := storage.NewPGXOIDCStore(mock)
	_, err = store.CreateOIDCSessionAndExchange(context.Background(), domain.OIDCSessionInput{Provider: "keycloak", Subject: "subject-1", Email: "linked@example.com", RefreshTokenHash: "refresh-hash", RefreshExpiresAt: refreshExpiresAt, AutoProvision: true}, func(_ domain.Session, user domain.LoginUser) (domain.OIDCExchangeInput, error) {
		return domain.OIDCExchangeInput{ID: "exchange-id", Provider: "keycloak", CodeHash: "code-hash", AccessValueEncrypted: "encrypted-access", RefreshValueEncrypted: "encrypted-refresh", BearerScheme: "Bearer", ExpiresIn: 900, User: user, ExpiresAt: exchangeExpiresAt}, nil
	})
	if err == nil {
		t.Fatal("expected exchange insert error")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXOIDCStore_CreateOIDCSessionAndExchangeReturnsCommitError(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()
	refreshExpiresAt := time.Now().Add(time.Hour)
	exchangeExpiresAt := time.Now().Add(2 * time.Minute)

	mock.ExpectBegin()
	expectOIDCPolicyQuery(mock)
	mock.ExpectQuery(`SELECT id, email::text, display_name, status, deleted_at`).
		WithArgs("keycloak", "subject-1").
		WillReturnRows(pgxmock.NewRows([]string{"id", "email", "display_name", "status", "deleted_at"}).AddRow("user-id", "linked@example.com", "Linked User", "active", nil))
	mock.ExpectExec(`INSERT INTO auth\.login_attempts`).
		WithArgs("user-id", "linked@example.com", true, nil, nil, nil).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectQuery(`INSERT INTO auth\.user_sessions`).
		WithArgs("user-id", nil, "refresh-hash", nil, nil, pgxmock.AnyArg(), refreshExpiresAt).
		WillReturnRows(pgxmock.NewRows([]string{"id", "user_id"}).AddRow("session-id", "user-id"))
	mock.ExpectExec(`INSERT INTO auth\.refresh_token_history`).
		WithArgs("session-id", "refresh-hash").
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectExec(`UPDATE auth\.users SET last_login_at`).
		WithArgs("user-id").
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectExec(`INSERT INTO auth\.oidc_exchange_codes`).
		WithArgs("exchange-id", "keycloak", "code-hash", "encrypted-access", "encrypted-refresh", "Bearer", 900, pgxmock.AnyArg(), exchangeExpiresAt).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectCommit().WillReturnError(errors.New("commit failed"))
	mock.ExpectRollback()

	store := storage.NewPGXOIDCStore(mock)
	_, err = store.CreateOIDCSessionAndExchange(context.Background(), domain.OIDCSessionInput{Provider: "keycloak", Subject: "subject-1", Email: "linked@example.com", RefreshTokenHash: "refresh-hash", RefreshExpiresAt: refreshExpiresAt, AutoProvision: true}, func(_ domain.Session, user domain.LoginUser) (domain.OIDCExchangeInput, error) {
		return domain.OIDCExchangeInput{ID: "exchange-id", Provider: "keycloak", CodeHash: "code-hash", AccessValueEncrypted: "encrypted-access", RefreshValueEncrypted: "encrypted-refresh", BearerScheme: "Bearer", ExpiresIn: 900, User: user, ExpiresAt: exchangeExpiresAt}, nil
	})
	if err == nil {
		t.Fatal("expected commit error")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXOIDCStore_ConsumeAuthRequestReturnsDatabaseError(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()
	mock.ExpectQuery(`UPDATE auth\.oidc_auth_requests`).
		WithArgs("keycloak", "state-hash").
		WillReturnError(errors.New("state query failed"))
	store := storage.NewPGXOIDCStore(mock)
	_, err = store.ConsumeAuthRequest(context.Background(), "keycloak", "state-hash")
	if err == nil {
		t.Fatal("expected state database error")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXOIDCStore_ConsumeExchangeReturnsDatabaseError(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()
	mock.ExpectQuery(`UPDATE auth\.oidc_exchange_codes`).
		WithArgs("keycloak", "code-hash").
		WillReturnError(errors.New("exchange query failed"))
	store := storage.NewPGXOIDCStore(mock)
	_, err = store.ConsumeExchange(context.Background(), "keycloak", "code-hash")
	if err == nil {
		t.Fatal("expected exchange database error")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXOIDCStore_CreateOIDCSessionAndExchangeReturnsLoginAttemptError(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectBegin()
	expectOIDCPolicyQuery(mock)
	expectOIDCLinkedUser(mock)
	mock.ExpectExec(`INSERT INTO auth\.login_attempts`).
		WithArgs("user-id", "linked@example.com", true, nil, nil, nil).
		WillReturnError(errors.New("audit insert failed"))
	mock.ExpectRollback()

	store := storage.NewPGXOIDCStore(mock)
	_, err = store.CreateOIDCSessionAndExchange(context.Background(), domain.OIDCSessionInput{Provider: "keycloak", Subject: "subject-1", Email: "linked@example.com", RefreshTokenHash: "refresh-hash", RefreshExpiresAt: time.Now().Add(time.Hour), AutoProvision: true}, func(domain.Session, domain.LoginUser) (domain.OIDCExchangeInput, error) {
		return domain.OIDCExchangeInput{}, nil
	})
	if err == nil {
		t.Fatal("expected login attempt error")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXOIDCStore_CreateOIDCSessionAndExchangeReturnsSessionInsertError(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()
	refreshExpiresAt := time.Now().Add(time.Hour)

	mock.ExpectBegin()
	expectOIDCPolicyQuery(mock)
	expectOIDCLinkedUser(mock)
	mock.ExpectExec(`INSERT INTO auth\.login_attempts`).
		WithArgs("user-id", "linked@example.com", true, nil, nil, nil).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectQuery(`INSERT INTO auth\.user_sessions`).
		WithArgs("user-id", nil, "refresh-hash", nil, nil, pgxmock.AnyArg(), refreshExpiresAt).
		WillReturnError(errors.New("session insert failed"))
	mock.ExpectRollback()

	store := storage.NewPGXOIDCStore(mock)
	_, err = store.CreateOIDCSessionAndExchange(context.Background(), domain.OIDCSessionInput{Provider: "keycloak", Subject: "subject-1", Email: "linked@example.com", RefreshTokenHash: "refresh-hash", RefreshExpiresAt: refreshExpiresAt, AutoProvision: true}, func(domain.Session, domain.LoginUser) (domain.OIDCExchangeInput, error) {
		return domain.OIDCExchangeInput{}, nil
	})
	if err == nil {
		t.Fatal("expected session insert error")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXOIDCStore_CreateOIDCSessionAndExchangeReturnsRefreshHistoryError(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()
	refreshExpiresAt := time.Now().Add(time.Hour)

	mock.ExpectBegin()
	expectOIDCPolicyQuery(mock)
	expectOIDCLinkedUser(mock)
	mock.ExpectExec(`INSERT INTO auth\.login_attempts`).
		WithArgs("user-id", "linked@example.com", true, nil, nil, nil).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectQuery(`INSERT INTO auth\.user_sessions`).
		WithArgs("user-id", nil, "refresh-hash", nil, nil, pgxmock.AnyArg(), refreshExpiresAt).
		WillReturnRows(pgxmock.NewRows([]string{"id", "user_id"}).AddRow("session-id", "user-id"))
	mock.ExpectExec(`INSERT INTO auth\.refresh_token_history`).
		WithArgs("session-id", "refresh-hash").
		WillReturnError(errors.New("history insert failed"))
	mock.ExpectRollback()

	store := storage.NewPGXOIDCStore(mock)
	_, err = store.CreateOIDCSessionAndExchange(context.Background(), domain.OIDCSessionInput{Provider: "keycloak", Subject: "subject-1", Email: "linked@example.com", RefreshTokenHash: "refresh-hash", RefreshExpiresAt: refreshExpiresAt, AutoProvision: true}, func(domain.Session, domain.LoginUser) (domain.OIDCExchangeInput, error) {
		return domain.OIDCExchangeInput{}, nil
	})
	if err == nil {
		t.Fatal("expected refresh history error")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXOIDCStore_CreateOIDCSessionAndExchangeReturnsLastLoginUpdateError(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()
	refreshExpiresAt := time.Now().Add(time.Hour)

	mock.ExpectBegin()
	expectOIDCPolicyQuery(mock)
	expectOIDCLinkedUser(mock)
	mock.ExpectExec(`INSERT INTO auth\.login_attempts`).
		WithArgs("user-id", "linked@example.com", true, nil, nil, nil).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectQuery(`INSERT INTO auth\.user_sessions`).
		WithArgs("user-id", nil, "refresh-hash", nil, nil, pgxmock.AnyArg(), refreshExpiresAt).
		WillReturnRows(pgxmock.NewRows([]string{"id", "user_id"}).AddRow("session-id", "user-id"))
	mock.ExpectExec(`INSERT INTO auth\.refresh_token_history`).
		WithArgs("session-id", "refresh-hash").
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectExec(`UPDATE auth\.users SET last_login_at`).
		WithArgs("user-id").
		WillReturnError(errors.New("last login update failed"))
	mock.ExpectRollback()

	store := storage.NewPGXOIDCStore(mock)
	_, err = store.CreateOIDCSessionAndExchange(context.Background(), domain.OIDCSessionInput{Provider: "keycloak", Subject: "subject-1", Email: "linked@example.com", RefreshTokenHash: "refresh-hash", RefreshExpiresAt: refreshExpiresAt, AutoProvision: true}, func(domain.Session, domain.LoginUser) (domain.OIDCExchangeInput, error) {
		return domain.OIDCExchangeInput{}, nil
	})
	if err == nil {
		t.Fatal("expected last login update error")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func expectOIDCLinkedUser(mock pgxmock.PgxPoolIface) {
	mock.ExpectQuery(`SELECT id, email::text, display_name, status, deleted_at`).
		WithArgs("keycloak", "subject-1").
		WillReturnRows(pgxmock.NewRows([]string{"id", "email", "display_name", "status", "deleted_at"}).AddRow("user-id", "linked@example.com", "Linked User", "active", nil))
}

func TestPGXOIDCStore_CreateOIDCSessionAndExchangeReturnsPolicyError(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT min_password_length`).WillReturnError(errors.New("policy lookup failed"))
	mock.ExpectRollback()

	store := storage.NewPGXOIDCStore(mock)
	_, err = store.CreateOIDCSessionAndExchange(context.Background(), domain.OIDCSessionInput{Provider: "keycloak", Subject: "subject-1", Email: "linked@example.com", AutoProvision: true}, func(domain.Session, domain.LoginUser) (domain.OIDCExchangeInput, error) {
		return domain.OIDCExchangeInput{}, nil
	})
	if err == nil {
		t.Fatal("expected policy lookup error")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// TestPGXOIDCStore_ConsumeExchangeRejectsSuspendedUser is a regression test for
// the suspension fix: a pre-suspension exchange code must not produce usable tokens
// after the user is suspended. The JOIN on auth.users with status='active' causes
// the UPDATE to match zero rows, returning ErrInvalidToken.
func TestPGXOIDCStore_ConsumeExchangeRejectsSuspendedUser(t *testing.T) {
mock, err := pgxmock.NewPool()
if err != nil {
t.Fatalf("pgxmock: %v", err)
}
defer mock.Close()

// User was suspended after the exchange code was minted.
// The JOIN on auth.users WHERE status='active' matches nothing → ErrNoRows.
mock.ExpectQuery(`UPDATE auth\.oidc_exchange_codes ec`).
WithArgs("keycloak", "code-hash").
WillReturnRows(pgxmock.NewRows([]string{
"id", "provider", "access_value_encrypted", "refresh_value_encrypted", "bearer_scheme", "expires_in", "user_json",
})) // empty: suspended user's join fails

store := storage.NewPGXOIDCStore(mock)
_, err = store.ConsumeExchange(context.Background(), "keycloak", "code-hash")
if !errors.Is(err, domain.ErrInvalidToken) {
t.Fatalf("expected ErrInvalidToken for suspended user, got %v", err)
}
if err := mock.ExpectationsWereMet(); err != nil {
t.Fatalf("unmet expectations: %v", err)
}
}
