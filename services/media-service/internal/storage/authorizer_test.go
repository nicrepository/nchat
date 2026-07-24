package storage

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/nicrepository/nchat/services/media-service/internal/domain"
	"github.com/nicrepository/nchat/services/media-service/internal/service"
	"github.com/pashagolub/pgxmock/v2"
)

const (
	storageTestUserID    = "11111111-1111-4111-8111-111111111111"
	storageTestSessionID = "22222222-2222-4222-8222-222222222222"
	storageTestResource  = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
)

func TestPGXResourceAuthorizerAllowsVisibleChannel(t *testing.T) {
	mock := newStorageMock(t)
	expiresAt := time.Now().UTC().Add(10 * time.Minute)
	mock.ExpectQuery(`(?s)WITH active_session AS.*auth\.user_sessions.*authorized_resource AS.*chat\.channels.*chat\.workspaces.*w\.status = 'active'.*chat\.workspace_members.*wm\.status = 'active'.*LEFT JOIN chat\.channel_members.*c\.status = 'active'.*c\.type = 'public'.*SELECT.*session_expires_at.*resource_id`).
		WithArgs(storageTestSessionID, storageTestUserID, storageTestResource).
		WillReturnRows(pgxmock.NewRows([]string{"session_expires_at", "resource_id"}).
			AddRow(expiresAt, storageTestResource))

	result, err := NewPGXResourceAuthorizer(mock).Authorize(context.Background(), service.AuthorizationInput{
		Kind: domain.ResourceKindChannel, ResourceID: storageTestResource,
		UserID: storageTestUserID, SessionID: storageTestSessionID,
	})
	if err != nil {
		t.Fatalf("authorize channel: %v", err)
	}
	if result.ID != storageTestResource || !result.SessionExpiresAt.Equal(expiresAt) {
		t.Fatalf("unexpected authorization: %+v", result)
	}
	assertStorageExpectations(t, mock)
}

func TestPGXResourceAuthorizerAllowsVisibleDM(t *testing.T) {
	mock := newStorageMock(t)
	expiresAt := time.Now().UTC().Add(10 * time.Minute)
	mock.ExpectQuery(`(?s)WITH active_session AS.*authorized_resource AS.*chat\.dm_conversations.*chat\.workspaces.*w\.status = 'active'.*chat\.workspace_members.*wm\.status = 'active'.*chat\.dm_members.*dm\.status = 'active'.*dc\.status = 'active'.*SELECT.*session_expires_at.*resource_id`).
		WithArgs(storageTestSessionID, storageTestUserID, storageTestResource).
		WillReturnRows(pgxmock.NewRows([]string{"session_expires_at", "resource_id"}).
			AddRow(expiresAt, storageTestResource))

	result, err := NewPGXResourceAuthorizer(mock).Authorize(context.Background(), service.AuthorizationInput{
		Kind: domain.ResourceKindDM, ResourceID: storageTestResource,
		UserID: storageTestUserID, SessionID: storageTestSessionID,
	})
	if err != nil {
		t.Fatalf("authorize DM: %v", err)
	}
	if result.ID != storageTestResource {
		t.Fatalf("unexpected authorization: %+v", result)
	}
	assertStorageExpectations(t, mock)
}

func TestPGXResourceAuthorizerMapsEveryInvalidSessionToUnauthorized(t *testing.T) {
	for _, state := range []string{
		"missing", "revoked", "idle expired", "absolute expired", "other user", "inactive user",
	} {
		t.Run(state, func(t *testing.T) {
			mock := newStorageMock(t)
			mock.ExpectQuery(`(?s)WITH active_session AS.*authorized_resource AS`).
				WithArgs(storageTestSessionID, storageTestUserID, storageTestResource).
				WillReturnRows(pgxmock.NewRows([]string{"session_expires_at", "resource_id"}).
					AddRow(nil, nil))
			_, err := NewPGXResourceAuthorizer(mock).Authorize(context.Background(), service.AuthorizationInput{
				Kind: domain.ResourceKindChannel, ResourceID: storageTestResource,
				UserID: storageTestUserID, SessionID: storageTestSessionID,
			})
			if !errors.Is(err, domain.ErrUnauthorized) {
				t.Fatalf("expected unauthorized for %s, got %v", state, err)
			}
			assertStorageExpectations(t, mock)
		})
	}
}

func TestPGXResourceAuthorizerDoesNotEnumerateInaccessibleResources(t *testing.T) {
	for _, state := range []string{"missing", "non-member", "cross-workspace", "inactive"} {
		t.Run(state, func(t *testing.T) {
			mock := newStorageMock(t)
			expiresAt := time.Now().UTC().Add(time.Minute)
			mock.ExpectQuery(`(?s)WITH active_session AS.*authorized_resource AS`).
				WithArgs(storageTestSessionID, storageTestUserID, storageTestResource).
				WillReturnRows(pgxmock.NewRows([]string{"session_expires_at", "resource_id"}).
					AddRow(expiresAt, nil))
			_, err := NewPGXResourceAuthorizer(mock).Authorize(context.Background(), service.AuthorizationInput{
				Kind: domain.ResourceKindDM, ResourceID: storageTestResource,
				UserID: storageTestUserID, SessionID: storageTestSessionID,
			})
			if !errors.Is(err, domain.ErrNotFound) {
				t.Fatalf("expected non-enumerating not found for %s, got %v", state, err)
			}
			assertStorageExpectations(t, mock)
		})
	}
}

func TestPGXResourceAuthorizerRejectsUnknownKindWithoutQuery(t *testing.T) {
	mock := newStorageMock(t)
	_, err := NewPGXResourceAuthorizer(mock).Authorize(context.Background(), service.AuthorizationInput{
		Kind: "group", ResourceID: storageTestResource,
		UserID: storageTestUserID, SessionID: storageTestSessionID,
	})
	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("expected invalid input, got %v", err)
	}
	assertStorageExpectations(t, mock)
}

func TestPGXResourceAuthorizerPropagatesDatabaseFailureAndCancellation(t *testing.T) {
	for _, expected := range []error{errors.New("database down"), context.Canceled} {
		mock := newStorageMock(t)
		mock.ExpectQuery(`(?s)WITH active_session AS.*authorized_resource AS`).
			WithArgs(storageTestSessionID, storageTestUserID, storageTestResource).
			WillReturnError(expected)
		_, err := NewPGXResourceAuthorizer(mock).Authorize(context.Background(), service.AuthorizationInput{
			Kind: domain.ResourceKindChannel, ResourceID: storageTestResource,
			UserID: storageTestUserID, SessionID: storageTestSessionID,
		})
		if !errors.Is(err, expected) {
			t.Fatalf("expected wrapped %v, got %v", expected, err)
		}
		assertStorageExpectations(t, mock)
	}
}

func newStorageMock(t *testing.T) pgxmock.PgxPoolIface {
	t.Helper()
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("new pgx mock: %v", err)
	}
	t.Cleanup(mock.Close)
	return mock
}

func assertStorageExpectations(t *testing.T, mock pgxmock.PgxPoolIface) {
	t.Helper()
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}
