package storage_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/nicrepository/nchat/services/auth-service/internal/domain"
	"github.com/nicrepository/nchat/services/auth-service/internal/storage"
)

// Two browsers finishing the Keycloak callback at the same instant for a
// subject that has never logged in run two provisioning transactions with no
// row to lock between them. The partial unique index on
// (external_provider, external_subject) is what keeps a second account from
// existing; this test asserts the loser of that race still logs in — into the
// same account — instead of surfacing the constraint violation as a 500.
//
// Only a real database can show this: the conflict is decided by Postgres, so
// a mocked pool would be asserting the fix rather than the behaviour.
func TestPGXOIDCStore_ConcurrentFirstLoginProvisionsExactlyOneUser(t *testing.T) {
	pool := connectAuthTestDB(t)
	applyAuthMigrations(t, pool)
	store := storage.NewPGXOIDCStore(pool)

	const subject = "concurrent-subject"
	const email = "concurrent@example.test"

	type outcome struct {
		created domain.OIDCCreatedSession
		err     error
	}
	results := make([]outcome, 2)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			results[i] = outcome{}
			created, err := store.CreateOIDCSessionAndExchange(
				context.Background(),
				oidcConcurrentInput(subject, email),
				oidcConcurrentExchangeBuilder(),
			)
			results[i] = outcome{created: created, err: err}
		}(i)
	}
	close(start)
	wg.Wait()

	for i, r := range results {
		if r.err != nil {
			t.Fatalf("login %d failed: %v", i, r.err)
		}
		if r.created.User.ID == "" || r.created.Session.ID == "" {
			t.Fatalf("login %d returned an empty session/user: %+v", i, r.created)
		}
	}
	if results[0].created.User.ID != results[1].created.User.ID {
		t.Fatalf("concurrent logins resolved to different users: %q vs %q",
			results[0].created.User.ID, results[1].created.User.ID)
	}
	if results[0].created.Session.ID == results[1].created.Session.ID {
		t.Fatalf("both logins reused session %q; each login must get its own session",
			results[0].created.Session.ID)
	}

	var users int
	if err := pool.QueryRow(context.Background(), `
		SELECT count(*) FROM auth.users
		WHERE auth_source = 'oidc' AND external_provider = 'keycloak' AND external_subject = $1`,
		subject,
	).Scan(&users); err != nil {
		t.Fatalf("count provisioned users: %v", err)
	}
	if users != 1 {
		t.Fatalf("auth.users holds %d rows for subject %q, want exactly 1", users, subject)
	}
}

// A concurrent first login whose e-mail already belongs to a manual account
// must still be refused. The conflict is invisible to the pre-insert lookup
// when the manual row is committed between the two statements, so the insert
// itself is the last line of defence and must not be read as "someone else
// provisioned this subject, log them in".
func TestPGXOIDCStore_InsertConflictOnManualEmailStaysAConflict(t *testing.T) {
	pool := connectAuthTestDB(t)
	applyAuthMigrations(t, pool)
	store := storage.NewPGXOIDCStore(pool)

	const email = "taken@example.test"
	insertActiveUser(t, pool, email)

	_, err := store.CreateOIDCSessionAndExchange(
		context.Background(),
		oidcConcurrentInput("subject-with-taken-email", email),
		oidcConcurrentExchangeBuilder(),
	)
	if err == nil {
		t.Fatal("expected the manual e-mail collision to be refused")
	}
	if !strings.Contains(err.Error(), domain.ErrOIDCAccountConflict.Error()) {
		t.Fatalf("want ErrOIDCAccountConflict, got %v", err)
	}
}

func oidcConcurrentInput(subject, email string) domain.OIDCSessionInput {
	return domain.OIDCSessionInput{
		Provider:              "keycloak",
		Subject:               subject,
		Email:                 email,
		DisplayName:           "Concurrent User",
		RefreshTokenHash:      uuid.NewString(),
		RefreshExpiresAt:      time.Now().UTC().Add(time.Hour),
		DeviceFingerprintHash: uuid.NewString(),
		DeviceName:            "device",
		Platform:              "web",
		AutoProvision:         true,
	}
}

func oidcConcurrentExchangeBuilder() func(domain.Session, domain.LoginUser) (domain.OIDCExchangeInput, error) {
	return func(session domain.Session, user domain.LoginUser) (domain.OIDCExchangeInput, error) {
		return domain.OIDCExchangeInput{
			ID:                    uuid.NewString(),
			Provider:              "keycloak",
			CodeHash:              uuid.NewString(),
			AccessValueEncrypted:  "encrypted-access",
			RefreshValueEncrypted: "encrypted-refresh",
			BearerScheme:          "Bearer",
			ExpiresIn:             900,
			User:                  user,
			ExpiresAt:             time.Now().UTC().Add(2 * time.Minute),
		}, nil
	}
}

// The application context (issue #578) decides which redirect URI a login run
// finishes against, so what the database accepts and returns for it is a
// security property, not a formatting detail. A mock confirms the SQL we send;
// only the database confirms the column's default, its constraint, and what a
// row written before the column existed now means.
func TestPGXOIDCStore_AppContextPostgreSQL(t *testing.T) {
	pool := connectAuthTestDB(t)
	applyAuthMigrations(t, pool)
	store := storage.NewPGXOIDCStore(pool)
	ctx := context.Background()

	newRequest := func(id, state string, app domain.OIDCAppContext) domain.OIDCLoginRequest {
		return domain.OIDCLoginRequest{
			ID:                    id,
			Provider:              "keycloak",
			StateHash:             state,
			NonceHash:             "nonce-" + id,
			PKCEVerifierEncrypted: "verifier-" + id,
			AppContext:            app,
			ExpiresAt:             time.Now().UTC().Add(10 * time.Minute),
		}
	}

	for _, app := range []domain.OIDCAppContext{domain.OIDCAppChat, domain.OIDCAppAdmin} {
		t.Run("round trip "+string(app), func(t *testing.T) {
			id := uuid.NewString()
			state := "state-" + id
			if err := store.CreateAuthRequest(ctx, newRequest(id, state, app)); err != nil {
				t.Fatalf("CreateAuthRequest: %v", err)
			}
			consumed, err := store.ConsumeAuthRequest(ctx, "keycloak", state)
			if err != nil {
				t.Fatalf("ConsumeAuthRequest: %v", err)
			}
			if consumed.AppContext != app {
				t.Fatalf("expected context %q, got %q", app, consumed.AppContext)
			}
		})
	}

	// A row written by the previous release — which knew nothing about the
	// column — must come back meaning the chat application. This is the
	// rolling-deployment case: old application, new database.
	t.Run("row written without the column defaults to chat", func(t *testing.T) {
		id := uuid.NewString()
		state := "state-legacy-" + id
		_, err := pool.Exec(ctx, `
			INSERT INTO auth.oidc_auth_requests
			  (id, provider, state_hash, nonce_hash, pkce_verifier_encrypted, expires_at)
			VALUES ($1, 'keycloak', $2, 'nonce', 'verifier', now() + interval '10 minutes')`,
			id, state)
		if err != nil {
			t.Fatalf("insert legacy row: %v", err)
		}

		consumed, err := store.ConsumeAuthRequest(ctx, "keycloak", state)
		if err != nil {
			t.Fatalf("ConsumeAuthRequest: %v", err)
		}
		if consumed.AppContext != domain.OIDCAppChat {
			t.Fatalf("expected the legacy default %q, got %q", domain.OIDCAppChat, consumed.AppContext)
		}
	})

	// The allowlist lives in the schema too, so no application bug and no hand
	// edit can put a context the service does not understand into a row the
	// callback will read.
	t.Run("unknown context is refused by the database", func(t *testing.T) {
		_, err := pool.Exec(ctx, `
			INSERT INTO auth.oidc_auth_requests
			  (id, provider, state_hash, nonce_hash, pkce_verifier_encrypted, app_context, expires_at)
			VALUES ($1, 'keycloak', $2, 'nonce', 'verifier', $3, now() + interval '10 minutes')`,
			uuid.NewString(), "state-invalid-"+uuid.NewString(), "https://evil.test")
		if err == nil {
			t.Fatal("expected the CHECK constraint to refuse an unknown context")
		}
	})

	// The column carries its own guarantees rather than relying on every writer
	// to remember them.
	t.Run("column is NOT NULL with a chat default", func(t *testing.T) {
		var isNullable, columnDefault string
		err := pool.QueryRow(ctx, `
			SELECT is_nullable, COALESCE(column_default, '')
			FROM information_schema.columns
			WHERE table_schema = 'auth'
			  AND table_name = 'oidc_auth_requests'
			  AND column_name = 'app_context'`).Scan(&isNullable, &columnDefault)
		if err != nil {
			t.Fatalf("inspect column: %v", err)
		}
		if isNullable != "NO" {
			t.Fatalf("expected NOT NULL, got is_nullable=%q", isNullable)
		}
		if !strings.HasPrefix(columnDefault, "'chat'") {
			t.Fatalf("expected a 'chat' default, got %q", columnDefault)
		}
	})
}
