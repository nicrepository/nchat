package storage_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	pgxmock "github.com/pashagolub/pgxmock/v2"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

const (
	readStateChannelID = "11111111-1111-4111-8111-111111111111"
	readStateMessageID = "22222222-2222-4222-8222-222222222222"
	readStateDMID      = "33333333-3333-4333-8333-333333333333"
)

var readStateColumns = []string{"target_type", "target_id", "last_read_at", "cursor_created_at", "cursor_message_id", "unread_count"}

func expectReadStateOf(mock pgxmock.PgxPoolIface, targetType, targetID string, rows *pgxmock.Rows) {
	mock.ExpectQuery(`(?s)SELECT '`+targetType+`', conv\.id::text, rs\.last_read_at, rs\.cursor_created_at.*AND conv\.id = \$3`).
		WithArgs("ws-1", "user-1", targetID).
		WillReturnRows(rows)
}

func TestPGXConversationReadStateStore_MarkReadResolvesTheMessageAndAdvancesMonotonically(t *testing.T) {
	mock := newMock(t)
	messageID := readStateMessageID
	// The cursor is the message's own (created_at, id), resolved inside the
	// authorized conversation, written to the cursor columns and only ever moved
	// forward; the legacy boundary is raised to just below the cursor's instant
	// and never lowered, and its informational id is left alone.
	mock.ExpectQuery(`(?s)WITH authorized AS.*FROM chat\.channels c.*channel_visible_to_user.*`+
		`target AS.*FROM chat\.messages m.*JOIN authorized a ON a\.workspace_id = m\.workspace_id AND a\.id = m\.channel_id.*`+
		`\$4::uuid IS NULL OR m\.id = \$4::uuid.*pending_link_scan.*ORDER BY m\.created_at DESC, m\.id DESC.*`+
		`INSERT INTO chat\.conversation_read_state AS rs.*last_read_at, cursor_created_at, cursor_message_id\).*`+
		`SELECT \$2, a\.workspace_id, a\.id, t\.created_at - interval '1 microsecond', t\.created_at, t\.id.*`+
		`ON CONFLICT \(user_id, channel_id\).*DO UPDATE.*`+
		`last_read_at = GREATEST\(rs\.last_read_at, EXCLUDED\.last_read_at\).*`+
		`WHERE rs\.cursor_created_at IS NULL\s*OR \(rs\.cursor_created_at, rs\.cursor_message_id\) < \(EXCLUDED\.cursor_created_at, EXCLUDED\.cursor_message_id\).*`+
		`SELECT EXISTS \(SELECT 1 FROM authorized\), EXISTS \(SELECT 1 FROM target\)`).
		WithArgs("ws-1", "user-1", readStateChannelID, &messageID).
		WillReturnRows(pgxmock.NewRows([]string{"allowed", "resolved"}).AddRow(true, true))
	readAt := time.Date(2026, 7, 15, 10, 0, 0, 0, time.UTC)
	boundary := readAt.Add(-time.Microsecond)
	expectReadStateOf(mock, "channel", readStateChannelID, pgxmock.NewRows(readStateColumns).
		AddRow("channel", readStateChannelID, &boundary, &readAt, &messageID, int64(2)))

	state, err := storage.NewPGXConversationReadStateStore(mock).MarkRead(
		context.Background(), "ws-1", "user-1", storage.ConversationReadTargetChannel, readStateChannelID, &messageID,
	)
	if err != nil {
		t.Fatalf("MarkRead: %v", err)
	}
	if state.UnreadCount != 2 || state.ReadThrough == nil || *state.ReadThrough.MessageID != messageID {
		t.Fatalf("post-write state = %+v", state)
	}
	checkExpectations(t, mock)
}

func TestPGXConversationReadStateStore_MarkReadDMIsIdempotentAndNonEnumerating(t *testing.T) {
	mock := newMock(t)
	for _, allowed := range []bool{true, true, false} {
		mock.ExpectQuery(`(?s)FROM chat\.dm_conversations dc.*chat\.dm_members dm.*a\.id = m\.dm_conversation_id.*INSERT INTO chat\.conversation_read_state.*ON CONFLICT \(user_id, dm_conversation_id\).*DO UPDATE.*SELECT EXISTS`).
			WithArgs("ws-1", "user-1", readStateDMID, (*string)(nil)).
			WillReturnRows(pgxmock.NewRows([]string{"allowed", "resolved"}).AddRow(allowed, allowed))
		if allowed {
			expectReadStateOf(mock, "dm", readStateDMID, pgxmock.NewRows(readStateColumns).
				AddRow("dm", readStateDMID, (*time.Time)(nil), (*time.Time)(nil), (*string)(nil), int64(0)))
		}
	}
	store := storage.NewPGXConversationReadStateStore(mock)
	for range 2 {
		if _, err := store.MarkRead(context.Background(), "ws-1", "user-1", storage.ConversationReadTargetDM, readStateDMID, nil); err != nil {
			t.Fatalf("idempotent MarkRead: %v", err)
		}
	}
	if _, err := store.MarkRead(context.Background(), "ws-1", "user-1", storage.ConversationReadTargetDM, readStateDMID, nil); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
	checkExpectations(t, mock)
}

func TestPGXConversationReadStateStore_MarkReadOutcomes(t *testing.T) {
	messageID := readStateMessageID
	cases := []struct {
		name              string
		lastReadMessageID *string
		allowed, resolved bool
		wantNotFound      bool
	}{
		// A message outside the conversation, invisible to the caller, or absent
		// is answered exactly like a conversation the caller cannot read.
		{name: "unresolved message", lastReadMessageID: &messageID, allowed: true, resolved: false, wantNotFound: true},
		{name: "unauthorized conversation", lastReadMessageID: &messageID, allowed: false, resolved: false, wantNotFound: true},
		// Marking an empty conversation read has nothing to advance to and is
		// not an error.
		{name: "mark all on an empty conversation", allowed: true, resolved: false},
		{name: "mark all", allowed: true, resolved: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mock := newMock(t)
			mock.ExpectQuery(`(?s)WITH authorized AS.*SELECT EXISTS`).
				WithArgs("ws-1", "user-1", readStateChannelID, tc.lastReadMessageID).
				WillReturnRows(pgxmock.NewRows([]string{"allowed", "resolved"}).AddRow(tc.allowed, tc.resolved))
			if !tc.wantNotFound {
				expectReadStateOf(mock, "channel", readStateChannelID, pgxmock.NewRows(readStateColumns))
			}
			_, err := storage.NewPGXConversationReadStateStore(mock).MarkRead(
				context.Background(), "ws-1", "user-1", storage.ConversationReadTargetChannel, readStateChannelID, tc.lastReadMessageID,
			)
			if got := errors.Is(err, domain.ErrNotFound); got != tc.wantNotFound || (!tc.wantNotFound && err != nil) {
				t.Fatalf("MarkRead error = %v, want not-found=%v", err, tc.wantNotFound)
			}
			checkExpectations(t, mock)
		})
	}
}

func TestPGXConversationReadStateStore_ReadStatesCountWhatNeitherClaimCovers(t *testing.T) {
	mock := newMock(t)
	// One predicate decides "not read": past the legacy boundary AND past the
	// message cursor, when there is one.
	mock.ExpectQuery(`(?s)SELECT 'channel'.*FROM chat\.messages m.*m\.status = 'active'.*m\.sender_id <> \$2.*`+
		`m\.created_at > COALESCE\(rs\.last_read_at, '-infinity'::timestamptz\)\s*`+
		`AND \(rs\.cursor_created_at IS NULL OR \(m\.created_at, m\.id\) > \(rs\.cursor_created_at, rs\.cursor_message_id\)\).*`+
		`chat\.channel_visible_to_user.*UNION ALL.*SELECT 'dm'.*chat\.dm_members`).
		WithArgs("ws-1", "user-1").
		WillReturnRows(pgxmock.NewRows(readStateColumns).
			// The cursor reaches past the boundary: it is the read point.
			AddRow("channel", "channel-1", ptr(at(9, 0)), ptr(at(10, 0)), ptr("msg-1"), int64(2)).
			// The boundary reaches past the cursor: an instant, no id.
			AddRow("dm", "dm-1", ptr(at(12, 0)), ptr(at(10, 0)), ptr("msg-2"), int64(1)).
			// Legacy only: an instant; the informational id never surfaces.
			AddRow("dm", "dm-2", ptr(at(9, 0)), (*time.Time)(nil), (*string)(nil), int64(3)).
			AddRow("dm", "dm-3", (*time.Time)(nil), (*time.Time)(nil), (*string)(nil), int64(5)))

	states, err := storage.NewPGXConversationReadStateStore(mock).ReadStates(context.Background(), "ws-1", "user-1")
	if err != nil {
		t.Fatalf("ReadStates: %v", err)
	}
	requireStoredState(t, states, "channel\x00channel-1", 2, ptr("msg-1"), at(10, 0))
	// The boundary reaches past the cursor: an instant, no id.
	requireStoredState(t, states, "dm\x00dm-1", 1, nil, at(12, 0))
	// Legacy only: an instant; the informational id never surfaces.
	requireStoredState(t, states, "dm\x00dm-2", 3, nil, at(9, 0))
	if never := states["dm\x00dm-3"]; never.UnreadCount != 5 || never.ReadThrough != nil {
		t.Fatalf("never-read state = %+v", never)
	}
	checkExpectations(t, mock)
}

// requireStoredState checks one listed state: its count, and a read point at
// `instant` naming `messageID` — or no message at all when it is nil.
func requireStoredState(t *testing.T, states map[string]domain.ConversationReadState, key string, unread int, messageID *string, instant time.Time) {
	t.Helper()
	state := states[key]
	point := state.ReadThrough
	if state.UnreadCount != unread || point == nil || !point.CreatedAt.Equal(instant) {
		t.Fatalf("%q state = %+v", key, state)
	}
	if (messageID == nil) != (point.MessageID == nil) || (messageID != nil && *messageID != *point.MessageID) {
		t.Fatalf("%q read point id = %v, want %v", key, point.MessageID, messageID)
	}
}

func at(hour, minute int) time.Time { return time.Date(2026, 7, 15, hour, minute, 0, 0, time.UTC) }

func TestPGXConversationReadStateStore_RejectsInvalidTargetAndWrapsDatabaseErrors(t *testing.T) {
	store := storage.NewPGXConversationReadStateStore(newMock(t))
	if _, err := store.MarkRead(context.Background(), "ws-1", "user-1", "group", "group-1", nil); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput, got %v", err)
	}

	mock := newMock(t)
	mock.ExpectQuery(`(?s)WITH authorized AS`).WithArgs("ws-1", "user-1", readStateChannelID, (*string)(nil)).WillReturnError(errors.New("database unavailable"))
	_, err := storage.NewPGXConversationReadStateStore(mock).MarkRead(context.Background(), "ws-1", "user-1", storage.ConversationReadTargetChannel, readStateChannelID, nil)
	if err == nil || !strings.Contains(err.Error(), "mark conversation read") {
		t.Fatalf("expected wrapped mark-read error, got %v", err)
	}

	mock = newMock(t)
	mock.ExpectQuery(`(?s)WITH authorized AS`).WithArgs("ws-1", "user-1", readStateChannelID, (*string)(nil)).
		WillReturnRows(pgxmock.NewRows([]string{"allowed", "resolved"}).AddRow(true, true))
	mock.ExpectQuery(`(?s)AND conv\.id = \$3`).WithArgs("ws-1", "user-1", readStateChannelID).WillReturnError(errors.New("database unavailable"))
	_, err = storage.NewPGXConversationReadStateStore(mock).MarkRead(context.Background(), "ws-1", "user-1", storage.ConversationReadTargetChannel, readStateChannelID, nil)
	if err == nil || !strings.Contains(err.Error(), "read conversation read state") {
		t.Fatalf("expected wrapped post-write read error, got %v", err)
	}

	mock = newMock(t)
	mock.ExpectQuery(`(?s)SELECT 'channel'.*UNION ALL.*SELECT 'dm'`).WithArgs("ws-1", "user-1").WillReturnError(errors.New("database unavailable"))
	_, err = storage.NewPGXConversationReadStateStore(mock).ReadStates(context.Background(), "ws-1", "user-1")
	if err == nil || !strings.Contains(err.Error(), "list conversation read states") {
		t.Fatalf("expected wrapped database error, got %v", err)
	}

	mock = newMock(t)
	mock.ExpectQuery(`(?s)SELECT 'channel'.*UNION ALL.*SELECT 'dm'`).WithArgs("ws-1", "user-1").
		WillReturnRows(pgxmock.NewRows([]string{"target_type"}).AddRow("channel"))
	_, err = storage.NewPGXConversationReadStateStore(mock).ReadStates(context.Background(), "ws-1", "user-1")
	if err == nil || !strings.Contains(err.Error(), "scan conversation read state") {
		t.Fatalf("expected wrapped scan error, got %v", err)
	}
}
