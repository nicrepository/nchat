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
	mock.ExpectQuery(`(?s)WITH active_session AS.*authorized_resource AS.*chat\.calls.*chat\.workspace_members.*status = 'active'.*target_type = 'user'.*caller_id = active\.user_id.*callee_id = active\.user_id.*target_type = 'channel'.*channel_visible_to_user.*target_type = 'dm'.*chat\.dm_members.*type = 'group'.*SELECT.*session_expires_at.*resource_id.*display_name`).
		WithArgs(storageTestSessionID, storageTestUserID, storageTestResource, storageTestParticipation).
		WillReturnRows(pgxmock.NewRows([]string{"session_expires_at", "resource_id", "display_name"}).
			AddRow(expiresAt, storageTestResource, "Ana Lima"))

	result, err := NewPGXResourceAuthorizer(mock).Authorize(context.Background(), service.AuthorizationInput{
		Kind: domain.ResourceKindCall, ResourceID: storageTestResource,
		UserID: storageTestUserID, SessionID: storageTestSessionID,
		ParticipationID: storageTestParticipation,
	})
	if err != nil || result.ID != storageTestResource {
		t.Fatalf("authorize active call: result=%+v err=%v", result, err)
	}
	assertStorageExpectations(t, mock)
}

// issue #622/#609: a resource (channel/group-DM) call token now requires a
// live participant lease in addition to membership/visibility — the query
// text itself must carry that requirement so a regression that silently
// drops the EXISTS clause is caught even by a mocked query-shape test.
func TestPGXResourceAuthorizerQueryRequiresLiveLeaseForResourceCalls(t *testing.T) {
	mock := newStorageMock(t)
	expiresAt := time.Now().UTC().Add(10 * time.Minute)
	mock.ExpectQuery(`(?s)target_type = 'channel'.*channel_visible_to_user.*chat\.call_participant_leases.*lease\.call_id = c\.id.*lease\.user_id = active\.user_id.*lease\.participation_id.*lease\.expires_at > clock_timestamp\(\).*target_type = 'dm'.*chat\.dm_members.*type = 'group'.*chat\.call_participant_leases.*lease\.call_id = c\.id.*lease\.user_id = active\.user_id.*lease\.participation_id.*lease\.expires_at > clock_timestamp\(\)`).
		WithArgs(storageTestSessionID, storageTestUserID, storageTestResource, storageTestParticipation).
		WillReturnRows(pgxmock.NewRows([]string{"session_expires_at", "resource_id", "display_name"}).
			AddRow(expiresAt, storageTestResource, "Ana Lima"))

	_, err := NewPGXResourceAuthorizer(mock).Authorize(context.Background(), service.AuthorizationInput{
		Kind: domain.ResourceKindCall, ResourceID: storageTestResource,
		UserID: storageTestUserID, SessionID: storageTestSessionID,
		ParticipationID: storageTestParticipation,
	})
	if err != nil {
		t.Fatalf("authorize call: %v", err)
	}
	assertStorageExpectations(t, mock)
}

// The direct (target_type = 'user') branch must carry no lease requirement
// at all — a regression that added one there would break every 1:1 call
// token, which never has a chat.call_participant_leases row.
func TestPGXResourceAuthorizerQueryDirectBranchCarriesNoLeaseRequirement(t *testing.T) {
	mock := newStorageMock(t)
	mock.ExpectQuery(`(?s)target_type = 'user' AND \(c\.caller_id = active\.user_id OR c\.callee_id = active\.user_id\)\)\s*OR`).
		WithArgs(storageTestSessionID, storageTestUserID, storageTestResource, "").
		WillReturnRows(pgxmock.NewRows([]string{"session_expires_at", "resource_id", "display_name"}).
			AddRow(nil, nil, nil))

	_, _ = NewPGXResourceAuthorizer(mock).Authorize(context.Background(), service.AuthorizationInput{
		Kind: domain.ResourceKindCall, ResourceID: storageTestResource,
		UserID: storageTestUserID, SessionID: storageTestSessionID,
	})
	assertStorageExpectations(t, mock)
}

func TestPGXResourceAuthorizerRejectsNonActiveOrNonParticipantCall(t *testing.T) {
	mock := newStorageMock(t)
	expiresAt := time.Now().UTC().Add(time.Minute)
	mock.ExpectQuery(`(?s)WITH active_session AS.*chat\.calls.*status = 'active'`).
		WithArgs(storageTestSessionID, storageTestUserID, storageTestResource, storageTestParticipation).
		WillReturnRows(pgxmock.NewRows([]string{"session_expires_at", "resource_id", "display_name"}).
			AddRow(expiresAt, nil, "Ana Lima"))

	_, err := NewPGXResourceAuthorizer(mock).Authorize(context.Background(), service.AuthorizationInput{
		Kind: domain.ResourceKindCall, ResourceID: storageTestResource,
		UserID: storageTestUserID, SessionID: storageTestSessionID,
		ParticipationID: storageTestParticipation,
	})
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("error = %v, want non-enumerating not found", err)
	}
	assertStorageExpectations(t, mock)
}
