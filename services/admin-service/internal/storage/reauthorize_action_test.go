package storage_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	pgxmock "github.com/pashagolub/pgxmock/v2"

	"github.com/nicrepository/nchat/services/admin-service/internal/domain"
	"github.com/nicrepository/nchat/services/admin-service/internal/storage"
)

// The lock-free re-authorization behind an external side effect (issue #582).
//
// It answers the same two ways the transactional check does, so a revocation
// observed at the last safe point looks to a client exactly like one the
// middleware caught.

func reauthorizationFor(capability domain.Capability) domain.MutationAuthorization {
	return domain.MutationAuthorization{
		SessionID:  "22222222-2222-2222-2222-222222222222",
		UserID:     "11111111-1111-1111-1111-111111111111",
		Capability: capability,
	}
}

func principalIDRows() *pgxmock.Rows {
	return pgxmock.NewRows([]string{"user_id"}).AddRow("11111111-1111-1111-1111-111111111111")
}

// The property this whole primitive exists for: it must not take a lock and
// must not open a transaction, because the caller is about to spend an unknown
// amount of time on somebody else's network. A lock held across that would make
// its duration a function of the relay, and would block the very revocation the
// check is looking for.
func TestReauthorizeAction_TakesNoLockAndNoTransaction(t *testing.T) {
	mock := newMock(t)
	mock.ExpectQuery(`FROM auth\.admin_sessions AS s`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(principalIDRows())
	mock.ExpectQuery(`FROM auth\.admin_principal_roles AS pr`).
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"capabilities"}).
			AddRow([]string{string(domain.CapabilityIntegrationsManage)}))

	err := storage.NewPGXAdminStore(mock).
		ReauthorizeAction(context.Background(), reauthorizationFor(domain.CapabilityIntegrationsManage))
	if err != nil {
		t.Fatalf("ReauthorizeAction: %v", err)
	}
	// No Begin was expected, so pgxmock fails the run if one happened; and the
	// predicate is asserted below to carry no FOR UPDATE.
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unexpected database interaction: %v", err)
	}
}

// The predicate is the same one the transactional path uses, minus the locks.
//
// Asserted against the SQL that actually ran rather than against a pattern that
// merely matches it: "carries no FOR UPDATE" is the security claim, and a
// regexp cannot express an absence. Sharing the predicate is what keeps a
// clause added to the revocation model from applying to only one of the two
// paths.
func TestReauthorizeAction_UsesTheSharedPredicateWithoutForUpdate(t *testing.T) {
	var executed []string
	mock, err := pgxmock.NewPool(pgxmock.QueryMatcherOption(
		pgxmock.QueryMatcherFunc(func(_, actual string) error {
			executed = append(executed, actual)
			return nil
		}),
	))
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectQuery("").WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).WillReturnRows(principalIDRows())
	mock.ExpectQuery("").WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"capabilities"}).
			AddRow([]string{string(domain.CapabilityIntegrationsManage)}))

	if err := storage.NewPGXAdminStore(mock).
		ReauthorizeAction(context.Background(), reauthorizationFor(domain.CapabilityIntegrationsManage)); err != nil {
		t.Fatalf("ReauthorizeAction: %v", err)
	}

	if len(executed) != 2 {
		t.Fatalf("expected two statements, got %d: %v", len(executed), executed)
	}
	for _, statement := range executed {
		if strings.Contains(strings.ToUpper(statement), "FOR UPDATE") {
			t.Fatalf("the lock-free re-authorization took a row lock: %s", statement)
		}
	}
	// Every revocation the model recognises is still checked.
	for _, clause := range []string{
		"s.revoked_at IS NULL",
		"s.idle_expires_at > now()",
		"s.absolute_expires_at > now()",
		"p.status = 'active'",
		"u.status = 'active'",
		"u.deleted_at IS NULL",
		"us.revoked_at IS NULL",
	} {
		if !strings.Contains(executed[0], clause) {
			t.Fatalf("the predicate no longer checks %q: %s", clause, executed[0])
		}
	}
}

// No matching row means the session, the login or the account is no longer
// usable. The caller must prove who they are again.
func TestReauthorizeAction_RefusesARevokedSessionAsUnauthorized(t *testing.T) {
	mock := newMock(t)
	mock.ExpectQuery(`FROM auth\.admin_sessions AS s`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnError(pgx.ErrNoRows)

	err := storage.NewPGXAdminStore(mock).
		ReauthorizeAction(context.Background(), reauthorizationFor(domain.CapabilityIntegrationsManage))
	if !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatalf("expected unauthorized, got %v", err)
	}
}

// The identity holds and the capability does not: the caller is known and still
// not allowed.
func TestReauthorizeAction_RefusesARemovedCapabilityAsForbidden(t *testing.T) {
	mock := newMock(t)
	mock.ExpectQuery(`FROM auth\.admin_sessions AS s`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(principalIDRows())
	mock.ExpectQuery(`FROM auth\.admin_principal_roles AS pr`).
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"capabilities"}).
			AddRow([]string{string(domain.CapabilityIntegrationsRead)}))

	err := storage.NewPGXAdminStore(mock).
		ReauthorizeAction(context.Background(), reauthorizationFor(domain.CapabilityIntegrationsManage))
	if !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("expected forbidden, got %v", err)
	}
}

// A caller that did not populate the authorization is refused rather than
// checked against nothing, and the database is never asked.
func TestReauthorizeAction_RefusesAnIncompleteAuthorization(t *testing.T) {
	for name, authorization := range map[string]domain.MutationAuthorization{
		"no session":         {UserID: "user-1", Capability: domain.CapabilityIntegrationsManage},
		"no user":            {SessionID: "session-1", Capability: domain.CapabilityIntegrationsManage},
		"unknown capability": {SessionID: "session-1", UserID: "user-1", Capability: "admin.nope"},
		"zero authorization": {},
	} {
		t.Run(name, func(t *testing.T) {
			mock := newMock(t)
			err := storage.NewPGXAdminStore(mock).ReauthorizeAction(context.Background(), authorization)
			if !errors.Is(err, domain.ErrForbidden) {
				t.Fatalf("expected forbidden, got %v", err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("the database was consulted for an incomplete authorization: %v", err)
			}
		})
	}
}

// A superuser holds every capability, including this one, by the same rule the
// middleware applies.
func TestReauthorizeAction_HonoursTheSuperuserGrant(t *testing.T) {
	mock := newMock(t)
	mock.ExpectQuery(`FROM auth\.admin_sessions AS s`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(principalIDRows())
	mock.ExpectQuery(`FROM auth\.admin_principal_roles AS pr`).
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"capabilities"}).
			AddRow([]string{string(domain.CapabilitySuperuser)}))

	err := storage.NewPGXAdminStore(mock).
		ReauthorizeAction(context.Background(), reauthorizationFor(domain.CapabilityIntegrationsManage))
	if err != nil {
		t.Fatalf("a superuser must still be authorized: %v", err)
	}
}

// A database failure is neither an authorization nor a refusal the client can
// act on: it propagates, and nothing about the query reaches the caller.
func TestReauthorizeAction_PropagatesADatabaseFailure(t *testing.T) {
	mock := newMock(t)
	mock.ExpectQuery(`FROM auth\.admin_sessions AS s`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnError(errors.New("connection reset"))

	err := storage.NewPGXAdminStore(mock).
		ReauthorizeAction(context.Background(), reauthorizationFor(domain.CapabilityIntegrationsManage))
	if err == nil || errors.Is(err, domain.ErrUnauthorized) || errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("expected a propagated failure, got %v", err)
	}
}

// An unwired store refuses rather than authorizing against nothing.
func TestReauthorizeAction_UnwiredStoreIsUnavailable(t *testing.T) {
	var store *storage.PGXAdminStore
	err := store.ReauthorizeAction(context.Background(), reauthorizationFor(domain.CapabilityIntegrationsManage))
	if !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("expected unavailable, got %v", err)
	}
}
