package storage_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/nicrepository/nchat/libs/go/platform/uploadpolicy"
	"github.com/nicrepository/nchat/services/file-service/internal/domain"
	"github.com/nicrepository/nchat/services/file-service/internal/service"
	"github.com/nicrepository/nchat/services/file-service/internal/storage"
)

const (
	testSessionID     = "11111111-1111-4111-8111-111111111111"
	testUserID        = "22222222-2222-4222-8222-222222222222"
	testChannelID     = "33333333-3333-4333-8333-333333333333"
	testWorkspaceID   = "44444444-4444-4444-8444-444444444444"
	testAttachmentID  = "55555555-5555-4555-8555-555555555555"
	testConversation  = "66666666-6666-4666-8666-666666666666"
	testStorageObject = "nchat/attachments/55555555-5555-4555-8555-555555555555"
	testPreviewObject = "77777777-7777-4777-8777-777777777777"
	testKEKKeyID      = "kek-test-active"
)

func text(value string) pgtype.Text { return pgtype.Text{String: value, Valid: true} }

func bigint(value int64) pgtype.Int8 { return pgtype.Int8{Int64: value, Valid: true} }

func timestamp(t time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: t, Valid: true}
}

func destinationInput(kind domain.DestinationKind, id string) service.DestinationAuthInput {
	return service.DestinationAuthInput{
		Destination: domain.Destination{Kind: kind, ID: id},
		UserID:      testUserID,
		SessionID:   testSessionID,
	}
}

func TestAuthorizeDestinationReturnsCanonicalWorkspace(t *testing.T) {
	expiry := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	tests := []struct {
		name string
		kind domain.DestinationKind
		id   string
	}{
		{name: "channel", kind: domain.DestinationKindChannel, id: testChannelID},
		{name: "dm", kind: domain.DestinationKindDM, id: testConversation},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pool := &fakePool{queryRow: func(string, ...any) pgx.Row {
				return valueRow{values: []any{timestamp(expiry), text(tt.id), text(testWorkspaceID), bigint(uploadpolicy.DefaultMaxUploadBytes)}}
			}}
			authorizer := storage.NewPGXDestinationAuthorizer(pool)

			authorized, err := authorizer.AuthorizeDestination(context.Background(),
				destinationInput(tt.kind, tt.id))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if authorized.ID != tt.id || authorized.WorkspaceID != testWorkspaceID {
				t.Fatalf("unexpected authorization: %+v", authorized)
			}
			if !authorized.SessionExpiresAt.Equal(expiry) {
				t.Fatalf("expected %v, got %v", expiry, authorized.SessionExpiresAt)
			}
			assertSessionScopedQuery(t, pool, tt.id)
		})
	}
}

// assertSessionScopedQuery pins the two properties that make the query safe:
// the session is validated inside it, and the destination id is a bound
// parameter rather than interpolated text.
func assertSessionScopedQuery(t *testing.T, pool *fakePool, destinationID string) {
	t.Helper()
	if !strings.Contains(pool.lastSQL, "active_session") {
		t.Fatal("authorization must validate the session in the same query")
	}
	if strings.Contains(pool.lastSQL, destinationID) {
		t.Fatal("the destination id must be a bound parameter, not interpolated SQL")
	}
	if len(pool.lastArgs) != 3 ||
		pool.lastArgs[0] != testSessionID ||
		pool.lastArgs[1] != testUserID ||
		pool.lastArgs[2] != destinationID {
		t.Fatalf("unexpected bound arguments: %v", pool.lastArgs)
	}
}

func TestChannelAuthorizationMatchesTheChatWritePolicy(t *testing.T) {
	pool := &fakePool{queryRow: func(string, ...any) pgx.Row {
		return valueRow{values: []any{timestamp(time.Now()), text(testChannelID), text(testWorkspaceID), bigint(uploadpolicy.DefaultMaxUploadBytes)}}
	}}
	if _, err := storage.NewPGXDestinationAuthorizer(pool).AuthorizeDestination(
		context.Background(), destinationInput(domain.DestinationKindChannel, testChannelID),
	); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, fragment := range []string{
		"chat.workspaces",
		"w.status = 'active'",
		"chat.workspace_members",
		"wm.status = 'active'",
		"c.status = 'active'",
		// The channel policy is not restated here: it is chat.channel_visible_to_user,
		// the one definition chat-service, file-service and media-service share, so
		// the RF-74 guest scope applies to uploads without a second copy of the rule.
		"chat.channel_visible_to_user(c.id, active.user_id)",
		"c.workspace_id",
	} {
		if !strings.Contains(pool.lastSQL, fragment) {
			t.Fatalf("channel policy must contain %q", fragment)
		}
	}
}

func TestDMAuthorizationRequiresActiveParticipation(t *testing.T) {
	pool := &fakePool{queryRow: func(string, ...any) pgx.Row {
		return valueRow{values: []any{timestamp(time.Now()), text(testConversation), text(testWorkspaceID), bigint(uploadpolicy.DefaultMaxUploadBytes)}}
	}}
	if _, err := storage.NewPGXDestinationAuthorizer(pool).AuthorizeDestination(
		context.Background(), destinationInput(domain.DestinationKindDM, testConversation),
	); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, fragment := range []string{
		"chat.dm_conversations",
		"chat.dm_members",
		"dm.status = 'active'",
		"dc.status = 'active'",
		"dc.workspace_id",
	} {
		if !strings.Contains(pool.lastSQL, fragment) {
			t.Fatalf("dm policy must contain %q", fragment)
		}
	}
}

func TestAuthorizeDestinationRejectsInvalidSession(t *testing.T) {
	pool := &fakePool{queryRow: func(string, ...any) pgx.Row {
		return valueRow{values: []any{pgtype.Timestamptz{}, pgtype.Text{}, pgtype.Text{}, pgtype.Int8{}}}
	}}
	_, err := storage.NewPGXDestinationAuthorizer(pool).AuthorizeDestination(
		context.Background(), destinationInput(domain.DestinationKindChannel, testChannelID))
	if !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatalf("expected ErrUnauthorized, got %v", err)
	}
}

// A valid session with no visible destination must not distinguish "absent"
// from "not yours": both are ErrNotFound.
func TestAuthorizeDestinationHidesInvisibleDestinations(t *testing.T) {
	tests := []struct {
		name   string
		values []any
	}{
		{name: "no destination", values: []any{timestamp(time.Now()), pgtype.Text{}, pgtype.Text{}, pgtype.Int8{}}},
		{name: "no workspace", values: []any{timestamp(time.Now()), text(testChannelID), pgtype.Text{}, pgtype.Int8{}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pool := &fakePool{queryRow: func(string, ...any) pgx.Row {
				return valueRow{values: tt.values}
			}}
			_, err := storage.NewPGXDestinationAuthorizer(pool).AuthorizeDestination(
				context.Background(), destinationInput(domain.DestinationKindChannel, testChannelID))
			if !errors.Is(err, domain.ErrNotFound) {
				t.Fatalf("expected ErrNotFound, got %v", err)
			}
		})
	}
}

func TestAuthorizeDestinationRejectsUnknownKind(t *testing.T) {
	authorizer := storage.NewPGXDestinationAuthorizer(&fakePool{})
	_, err := authorizer.AuthorizeDestination(context.Background(),
		destinationInput("workspace", testChannelID))
	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput, got %v", err)
	}
}

func TestAuthorizeDestinationWithoutPoolIsUnavailable(t *testing.T) {
	for _, authorizer := range []*storage.PGXDestinationAuthorizer{
		nil, storage.NewPGXDestinationAuthorizer(nil),
	} {
		_, err := authorizer.AuthorizeDestination(context.Background(),
			destinationInput(domain.DestinationKindChannel, testChannelID))
		if !errors.Is(err, domain.ErrUnavailable) {
			t.Fatalf("expected ErrUnavailable, got %v", err)
		}
	}
}

func TestAuthorizeDestinationWrapsDatabaseFailures(t *testing.T) {
	dbErr := errors.New("connection reset")
	pool := &fakePool{queryRow: func(string, ...any) pgx.Row { return errRow{err: dbErr} }}
	_, err := storage.NewPGXDestinationAuthorizer(pool).AuthorizeDestination(
		context.Background(), destinationInput(domain.DestinationKindChannel, testChannelID))
	if !errors.Is(err, dbErr) {
		t.Fatalf("expected the database error to be wrapped, got %v", err)
	}
	if errors.Is(err, domain.ErrNotFound) || errors.Is(err, domain.ErrUnauthorized) {
		t.Fatal("a database failure must not be reported as a client error")
	}
}
