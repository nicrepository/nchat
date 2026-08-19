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
