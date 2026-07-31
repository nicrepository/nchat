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

func TestPGXResourceAuthorizerRequiresActiveCallParticipant(t *testing.T) {
	mock := newStorageMock(t)
	expiresAt := time.Now().UTC().Add(10 * time.Minute)
	mock.ExpectQuery(`(?s)WITH active_session AS.*authorized_resource AS.*chat\.calls.*chat\.workspace_members.*status = 'active'.*caller_id = active\.user_id.*callee_id = active\.user_id.*SELECT.*session_expires_at.*resource_id`).
		WithArgs(storageTestSessionID, storageTestUserID, storageTestResource).
		WillReturnRows(pgxmock.NewRows([]string{"session_expires_at", "resource_id"}).
			AddRow(expiresAt, storageTestResource))

	result, err := NewPGXResourceAuthorizer(mock).Authorize(context.Background(), service.AuthorizationInput{
		Kind: domain.ResourceKindCall, ResourceID: storageTestResource,
		UserID: storageTestUserID, SessionID: storageTestSessionID,
	})
	if err != nil || result.ID != storageTestResource {
		t.Fatalf("authorize active call: result=%+v err=%v", result, err)
	}
	assertStorageExpectations(t, mock)
}

func TestPGXResourceAuthorizerRejectsNonActiveOrNonParticipantCall(t *testing.T) {
	mock := newStorageMock(t)
	expiresAt := time.Now().UTC().Add(time.Minute)
	mock.ExpectQuery(`(?s)WITH active_session AS.*chat\.calls.*status = 'active'`).
		WithArgs(storageTestSessionID, storageTestUserID, storageTestResource).
		WillReturnRows(pgxmock.NewRows([]string{"session_expires_at", "resource_id"}).AddRow(expiresAt, nil))

	_, err := NewPGXResourceAuthorizer(mock).Authorize(context.Background(), service.AuthorizationInput{
		Kind: domain.ResourceKindCall, ResourceID: storageTestResource,
		UserID: storageTestUserID, SessionID: storageTestSessionID,
	})
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("error = %v, want non-enumerating not found", err)
	}
	assertStorageExpectations(t, mock)
}
