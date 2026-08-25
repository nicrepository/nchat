package storage_test

import (
	"context"
	"errors"
	"testing"

	pgxmock "github.com/pashagolub/pgxmock/v2"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

// Muting, at the statement level (issue #527).
//
// The behaviour against a real database is proven in
// notification_pref_store_postgres_test.go. What these add is the half that does
// not need one: that each target kind reaches its own authorization statement,
// that an unknown kind reaches none, and that a statement admitting nothing is
// reported as the same non-enumerating not-found rather than as success.

func TestPGXNotificationPrefStore_Mute_ChannelUsesTheGeneralGuardedStatement(t *testing.T) {
	mock := newMock(t)
	// The three conditions that decide a channel mute, and the third is the
	// structural one: #geral is refused in SQL, by the column and never by name.
	mock.ExpectQuery(`(?s)WITH authorized AS.*c\.is_general = false.*chat\.channel_visible_to_user\(c\.id, \$2::uuid\).*INSERT INTO chat\.conversation_notification_prefs.*ON CONFLICT \(user_id, channel_id\).*SELECT EXISTS`).
		WithArgs("ws-1", "user-1", "chan-1").
		WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(true))

	store := storage.NewPGXNotificationPrefStore(mock)
	if err := store.Mute(context.Background(), "ws-1", "user-1", storage.NotificationPrefTargetChannel, "chan-1"); err != nil {
		t.Fatalf("Mute: %v", err)
	}
	checkExpectations(t, mock)
}

func TestPGXNotificationPrefStore_Mute_DMUsesTheParticipationStatement(t *testing.T) {
	mock := newMock(t)
	// A conversation is silenceable for whoever is in it, so participation is
	// the whole rule and there is no general-channel analogue.
	mock.ExpectQuery(`(?s)WITH authorized AS.*chat\.dm_members dm.*dm\.status = 'active'.*INSERT INTO chat\.conversation_notification_prefs.*dm_conversation_id.*SELECT EXISTS`).
		WithArgs("ws-1", "user-1", "dm-1").
		WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(true))

	store := storage.NewPGXNotificationPrefStore(mock)
	if err := store.Mute(context.Background(), "ws-1", "user-1", storage.NotificationPrefTargetDM, "dm-1"); err != nil {
		t.Fatalf("Mute: %v", err)
	}
	checkExpectations(t, mock)
}

// One answer for "no such conversation", "you cannot see it" and "it is the
// general channel": the first two must stay indistinguishable so the endpoint
// cannot be used to probe which IDs exist.
func TestPGXNotificationPrefStore_Mute_UnauthorizedIsNonEnumeratingNotFound(t *testing.T) {
	mock := newMock(t)
	mock.ExpectQuery(`WITH authorized AS`).
		WithArgs("ws-1", "user-1", "chan-1").
		WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(false))

	store := storage.NewPGXNotificationPrefStore(mock)
	err := store.Mute(context.Background(), "ws-1", "user-1", storage.NotificationPrefTargetChannel, "chan-1")
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("Mute error = %v, want ErrNotFound", err)
	}
	checkExpectations(t, mock)
}

func TestPGXNotificationPrefStore_Mute_QueryFailurePropagates(t *testing.T) {
	mock := newMock(t)
	mock.ExpectQuery(`WITH authorized AS`).WillReturnError(errors.New("db down"))

	store := storage.NewPGXNotificationPrefStore(mock)
	if err := store.Mute(context.Background(), "ws-1", "user-1", storage.NotificationPrefTargetDM, "dm-1"); err == nil {
		t.Fatal("expected the database failure to propagate")
	}
}

// An unknown target kind never reaches the database: it is a caller mistake, not
// a lookup that happens to find nothing.
func TestPGXNotificationPrefStore_RejectsAnUnknownTargetKindWithoutQuerying(t *testing.T) {
	mock := newMock(t)
	store := storage.NewPGXNotificationPrefStore(mock)

	if err := store.Mute(context.Background(), "ws-1", "user-1", "workspace", "x"); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("Mute error = %v, want ErrInvalidInput", err)
	}
	if err := store.Unmute(context.Background(), "user-1", "workspace", "x"); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("Unmute error = %v, want ErrInvalidInput", err)
	}
	checkExpectations(t, mock)
}

// Unmute is deliberately unguarded by visibility: a user must always be able to
// undo their own preference, even for a conversation they can no longer see. The
// row it deletes is therefore keyed by the user and the target and nothing else.
func TestPGXNotificationPrefStore_Unmute_DeletesOnlyTheCallersOwnRow(t *testing.T) {
	for _, test := range []struct {
		name       string
		targetType string
		column     string
	}{
		{name: "channel", targetType: storage.NotificationPrefTargetChannel, column: `channel_id`},
		{name: "dm", targetType: storage.NotificationPrefTargetDM, column: `dm_conversation_id`},
	} {
		t.Run(test.name, func(t *testing.T) {
			mock := newMock(t)
			mock.ExpectExec(`DELETE FROM chat\.conversation_notification_prefs WHERE user_id = \$1 AND `+test.column+` = \$2`).
				WithArgs("user-1", "target-1").
				WillReturnResult(pgxmock.NewResult("DELETE", 1))

			store := storage.NewPGXNotificationPrefStore(mock)
			if err := store.Unmute(context.Background(), "user-1", test.targetType, "target-1"); err != nil {
				t.Fatalf("Unmute: %v", err)
			}
			checkExpectations(t, mock)
		})
	}
}

func TestPGXNotificationPrefStore_Unmute_ExecFailurePropagates(t *testing.T) {
	mock := newMock(t)
	mock.ExpectExec(`DELETE FROM chat\.conversation_notification_prefs`).WillReturnError(errors.New("db down"))

	store := storage.NewPGXNotificationPrefStore(mock)
	if err := store.Unmute(context.Background(), "user-1", storage.NotificationPrefTargetChannel, "chan-1"); err == nil {
		t.Fatal("expected the database failure to propagate")
	}
}

// The listing is re-filtered by current visibility, so a preference that points
// at something the user can no longer see is simply not returned — the row stays
// and stops being served, exactly like the favourites listing.
func TestPGXNotificationPrefStore_ListMuted_ReturnsBothKinds(t *testing.T) {
	mock := newMock(t)
	mock.ExpectQuery(`(?s)SELECT 'channel'.*chat\.channel_visible_to_user\(c\.id, \$2::uuid\).*UNION ALL.*SELECT 'dm'.*chat\.dm_members dm`).
		WithArgs("ws-1", "user-1").
		WillReturnRows(pgxmock.NewRows([]string{"target_type", "target_id"}).
			AddRow("channel", "chan-1").
			AddRow("dm", "dm-1"))

	store := storage.NewPGXNotificationPrefStore(mock)
	muted, err := store.ListMuted(context.Background(), "ws-1", "user-1")
	if err != nil {
		t.Fatalf("ListMuted: %v", err)
	}
	if len(muted) != 2 || muted[0].TargetType != "channel" || muted[0].TargetID != "chan-1" ||
		muted[1].TargetType != "dm" || muted[1].TargetID != "dm-1" {
		t.Fatalf("muted = %+v, want one channel and one dm", muted)
	}
	checkExpectations(t, mock)
}

// An empty result is an empty slice and never nil: the sidebar builds a lookup
// map from it on every request.
func TestPGXNotificationPrefStore_ListMuted_EmptyIsNotNil(t *testing.T) {
	mock := newMock(t)
	mock.ExpectQuery(`SELECT 'channel'`).
		WithArgs("ws-1", "user-1").
		WillReturnRows(pgxmock.NewRows([]string{"target_type", "target_id"}))

	store := storage.NewPGXNotificationPrefStore(mock)
	muted, err := store.ListMuted(context.Background(), "ws-1", "user-1")
	if err != nil {
		t.Fatalf("ListMuted: %v", err)
	}
	if muted == nil || len(muted) != 0 {
		t.Fatalf("muted = %#v, want an empty non-nil slice", muted)
	}
	checkExpectations(t, mock)
}

func TestPGXNotificationPrefStore_ListMuted_FailuresPropagate(t *testing.T) {
	t.Run("query", func(t *testing.T) {
		mock := newMock(t)
		mock.ExpectQuery(`SELECT 'channel'`).WillReturnError(errors.New("db down"))

		if _, err := storage.NewPGXNotificationPrefStore(mock).ListMuted(context.Background(), "ws-1", "user-1"); err == nil {
			t.Fatal("expected the database failure to propagate")
		}
	})

	t.Run("scan", func(t *testing.T) {
		mock := newMock(t)
		mock.ExpectQuery(`SELECT 'channel'`).
			WithArgs("ws-1", "user-1").
			WillReturnRows(pgxmock.NewRows([]string{"target_type", "target_id"}).AddRow(42, "chan-1"))

		if _, err := storage.NewPGXNotificationPrefStore(mock).ListMuted(context.Background(), "ws-1", "user-1"); err == nil {
			t.Fatal("expected the scan failure to propagate")
		}
	})
}
