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
			// ONE statement, and the shape is the whole point (issue #136,
			// round 2). The two data-modifying CTEs partition the rows by
			// level — the UPDATE takes only `<> 'all'` and the DELETE only
			// `= 'all'` — so no interleaving with a concurrent Mute can leave
			// `all` with a NULL muted_at. Two round trips could, and did.
			//
			// Nothing here writes notification_level, which is what preserves
			// it.
			mock.ExpectExec(`(?s)WITH cleared AS \(\s*UPDATE chat\.conversation_notification_prefs.*SET muted_at = NULL.*user_id = \$1 AND `+test.column+` = \$2.*notification_level <> 'all'.*\).*DELETE FROM chat\.conversation_notification_prefs.*user_id = \$1 AND `+test.column+` = \$2.*notification_level = 'all'`).
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

// Unmuting must never be two statements again: a second Exec is the regression
// this asserts against, because the race it reopens is invisible to every
// single-threaded test.
func TestPGXNotificationPrefStore_Unmute_IssuesExactlyOneStatement(t *testing.T) {
	mock := newMock(t)
	// Exactly one expectation is registered. pgxmock fails any further call, so
	// a two-round-trip Unmute cannot pass this test.
	mock.ExpectExec(`WITH cleared AS`).
		WithArgs("user-1", "chan-1").
		WillReturnResult(pgxmock.NewResult("DELETE", 0))

	store := storage.NewPGXNotificationPrefStore(mock)
	if err := store.Unmute(context.Background(), "user-1",
		storage.NotificationPrefTargetChannel, "chan-1"); err != nil {
		t.Fatalf("Unmute: %v", err)
	}
	checkExpectations(t, mock)
}

func TestPGXNotificationPrefStore_Unmute_ExecFailurePropagates(t *testing.T) {
	mock := newMock(t)
	mock.ExpectExec(`WITH cleared AS`).WillReturnError(errors.New("db down"))

	store := storage.NewPGXNotificationPrefStore(mock)
	if err := store.Unmute(context.Background(), "user-1", storage.NotificationPrefTargetChannel, "chan-1"); err == nil {
		t.Fatal("expected the database failure to propagate")
	}
}

// prefListingColumns is the listing's projection, which since issue #136 is
// four columns: a row no longer means "muted", so the mute and the level both
// have to travel.
func prefListingColumns() []string {
	return []string{"target_type", "target_id", "notification_level", "muted"}
}

// The listing is re-filtered by current visibility, so a preference that points
// at something the user can no longer see is simply not returned — the row stays
// and stops being served, exactly like the favourites listing.
func TestPGXNotificationPrefStore_ListPreferences_ReturnsBothKinds(t *testing.T) {
	mock := newMock(t)
	mock.ExpectQuery(`(?s)SELECT 'channel'.*chat\.channel_visible_to_user\(c\.id, \$2::uuid\).*UNION ALL.*SELECT 'dm'.*chat\.dm_members dm`).
		WithArgs("ws-1", "user-1").
		WillReturnRows(pgxmock.NewRows(prefListingColumns()).
			AddRow("channel", "chan-1", "all", true).
			AddRow("dm", "dm-1", "mentions_replies", false))

	store := storage.NewPGXNotificationPrefStore(mock)
	prefs, err := store.ListPreferences(context.Background(), "ws-1", "user-1")
	if err != nil {
		t.Fatalf("ListPreferences: %v", err)
	}
	if len(prefs) != 2 || prefs[0].TargetType != "channel" || prefs[0].TargetID != "chan-1" ||
		prefs[1].TargetType != "dm" || prefs[1].TargetID != "dm-1" {
		t.Fatalf("prefs = %+v, want one channel and one dm", prefs)
	}
	// Both halves survive the projection, in both combinations.
	if !prefs[0].Muted || prefs[0].Level != storage.NotificationLevelAll {
		t.Fatalf("channel pref = %+v, want silenced at the default level", prefs[0])
	}
	if prefs[1].Muted || prefs[1].Level != storage.NotificationLevelMentionsReplies {
		t.Fatalf("dm pref = %+v, want mentions_replies and unsilenced", prefs[1])
	}
	checkExpectations(t, mock)
}

// The mute is the timestamp and no longer the existence of the row (issue #136).
// A row with a NULL muted_at is somebody who asked to keep hearing about
// mentions, and reading its presence as a mute would silence them.
func TestPGXNotificationPrefStore_ListPreferences_ReadsMutedFromTheTimestamp(t *testing.T) {
	mock := newMock(t)
	mock.ExpectQuery(`\(p\.muted_at IS NOT NULL\)`).
		WithArgs("ws-1", "user-1").
		WillReturnRows(pgxmock.NewRows(prefListingColumns()).
			AddRow("channel", "chan-1", "mentions_replies", false))

	prefs, err := storage.NewPGXNotificationPrefStore(mock).
		ListPreferences(context.Background(), "ws-1", "user-1")
	if err != nil {
		t.Fatalf("ListPreferences: %v", err)
	}
	if len(prefs) != 1 || prefs[0].Muted {
		t.Fatalf("prefs = %+v, want an existing row reported as not muted", prefs)
	}
	checkExpectations(t, mock)
}

// An empty result is an empty slice and never nil: the sidebar builds a lookup
// map from it on every request.
func TestPGXNotificationPrefStore_ListPreferences_EmptyIsNotNil(t *testing.T) {
	mock := newMock(t)
	mock.ExpectQuery(`SELECT 'channel'`).
		WithArgs("ws-1", "user-1").
		WillReturnRows(pgxmock.NewRows(prefListingColumns()))

	store := storage.NewPGXNotificationPrefStore(mock)
	prefs, err := store.ListPreferences(context.Background(), "ws-1", "user-1")
	if err != nil {
		t.Fatalf("ListPreferences: %v", err)
	}
	if prefs == nil || len(prefs) != 0 {
		t.Fatalf("prefs = %#v, want an empty non-nil slice", prefs)
	}
	checkExpectations(t, mock)
}

func TestPGXNotificationPrefStore_ListPreferences_FailuresPropagate(t *testing.T) {
	t.Run("query", func(t *testing.T) {
		mock := newMock(t)
		mock.ExpectQuery(`SELECT 'channel'`).WillReturnError(errors.New("db down"))

		if _, err := storage.NewPGXNotificationPrefStore(mock).ListPreferences(context.Background(), "ws-1", "user-1"); err == nil {
			t.Fatal("expected the database failure to propagate")
		}
	})

	t.Run("scan", func(t *testing.T) {
		mock := newMock(t)
		mock.ExpectQuery(`SELECT 'channel'`).
			WithArgs("ws-1", "user-1").
			WillReturnRows(pgxmock.NewRows(prefListingColumns()).AddRow(42, "chan-1", "all", true))

		if _, err := storage.NewPGXNotificationPrefStore(mock).ListPreferences(context.Background(), "ws-1", "user-1"); err == nil {
			t.Fatal("expected the scan failure to propagate")
		}
	})
}

// The mute statement writes muted_at and never notification_level, and that
// omission is the non-destructive guarantee in SQL (issue #136).
func TestPGXNotificationPrefStore_Mute_NeverWritesTheLevel(t *testing.T) {
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
			mock.ExpectQuery(`(?s)INSERT INTO chat\.conversation_notification_prefs AS p\s+\(user_id, workspace_id, `+test.column+`, muted_at\).*DO UPDATE SET muted_at = COALESCE\(p\.muted_at, now\(\)\)`).
				WithArgs("ws-1", "user-1", "target-1").
				WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(true))

			store := storage.NewPGXNotificationPrefStore(mock)
			if err := store.Mute(context.Background(), "ws-1", "user-1", test.targetType, "target-1"); err != nil {
				t.Fatalf("Mute: %v", err)
			}
			checkExpectations(t, mock)
		})
	}
}

// The level statement carries the level as an argument and clears the mute, and
// its channel form has no general-channel guard: #geral may be narrowed to
// mentions and replies, it just may not be silenced (issue #136).
func TestPGXNotificationPrefStore_SetLevel_WritesTheLevelAndClearsTheMute(t *testing.T) {
	mock := newMock(t)
	mock.ExpectQuery(`(?s)WITH authorized AS.*chat\.channel_visible_to_user\(c\.id, \$2::uuid\).*notification_level, muted_at\).*\$4, NULL.*DO UPDATE SET notification_level = \$4, muted_at = NULL`).
		WithArgs("ws-1", "user-1", "chan-1", storage.NotificationLevelMentionsReplies).
		WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(true))

	store := storage.NewPGXNotificationPrefStore(mock)
	if err := store.SetLevel(context.Background(), "ws-1", "user-1",
		storage.NotificationPrefTargetChannel, "chan-1", storage.NotificationLevelMentionsReplies); err != nil {
		t.Fatalf("SetLevel: %v", err)
	}
	checkExpectations(t, mock)
}

// #geral is refused a mute and not a level, so the level statement must not
// carry the guard the mute statement does. Asserted on the statement text
// because that one condition is the entire difference between them.
func TestPGXNotificationPrefStore_SetLevel_HasNoGeneralChannelGuard(t *testing.T) {
	mock := newMock(t)
	mock.ExpectQuery(`WITH authorized AS`).
		WithArgs("ws-1", "user-1", "chan-1", storage.NotificationLevelMentionsReplies).
		WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(true))
	store := storage.NewPGXNotificationPrefStore(mock)
	if err := store.SetLevel(context.Background(), "ws-1", "user-1",
		storage.NotificationPrefTargetChannel, "chan-1", storage.NotificationLevelMentionsReplies); err != nil {
		t.Fatalf("SetLevel: %v", err)
	}
	// The mute path, by contrast, must still refuse it in SQL.
	muteMock := newMock(t)
	muteMock.ExpectQuery(`c\.is_general = false`).
		WithArgs("ws-1", "user-1", "chan-1").
		WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(true))
	if err := storage.NewPGXNotificationPrefStore(muteMock).
		Mute(context.Background(), "ws-1", "user-1", storage.NotificationPrefTargetChannel, "chan-1"); err != nil {
		t.Fatalf("Mute: %v", err)
	}
	checkExpectations(t, mock)
	checkExpectations(t, muteMock)
}

// The default level with no mute is the absence of a row, so asking for it
// deletes rather than writing — and, like Unmute, needs no visibility check
// because it can only remove the caller's own row.
func TestPGXNotificationPrefStore_SetLevel_AllRemovesTheRow(t *testing.T) {
	mock := newMock(t)
	mock.ExpectExec(`DELETE FROM chat\.conversation_notification_prefs\s+WHERE user_id = \$1 AND channel_id = \$2$`).
		WithArgs("user-1", "chan-1").
		WillReturnResult(pgxmock.NewResult("DELETE", 1))

	store := storage.NewPGXNotificationPrefStore(mock)
	if err := store.SetLevel(context.Background(), "ws-1", "user-1",
		storage.NotificationPrefTargetChannel, "chan-1", storage.NotificationLevelAll); err != nil {
		t.Fatalf("SetLevel(all): %v", err)
	}
	checkExpectations(t, mock)
}

// An unknown level never reaches a statement at all.
func TestPGXNotificationPrefStore_SetLevel_RejectsAnUnknownLevel(t *testing.T) {
	mock := newMock(t)
	store := storage.NewPGXNotificationPrefStore(mock)
	if err := store.SetLevel(context.Background(), "ws-1", "user-1",
		storage.NotificationPrefTargetChannel, "chan-1", "everything_always"); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("SetLevel error = %v, want ErrInvalidInput", err)
	}
	checkExpectations(t, mock)
}

// An unauthorized level write is the same non-enumerating refusal the mute is.
func TestPGXNotificationPrefStore_SetLevel_UnauthorizedIsNonEnumeratingNotFound(t *testing.T) {
	mock := newMock(t)
	mock.ExpectQuery(`WITH authorized AS`).
		WithArgs("ws-1", "user-1", "chan-1", storage.NotificationLevelMentionsReplies).
		WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(false))

	store := storage.NewPGXNotificationPrefStore(mock)
	if err := store.SetLevel(context.Background(), "ws-1", "user-1",
		storage.NotificationPrefTargetChannel, "chan-1", storage.NotificationLevelMentionsReplies); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("SetLevel error = %v, want ErrNotFound", err)
	}
	checkExpectations(t, mock)
}

// The fan-out's read projects both halves, for the recipients who expressed
// anything, keyed by the (user, target) partial unique index.
func TestPGXNotificationPrefStore_PreferencesForUsers_ProjectsBothHalves(t *testing.T) {
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
			mock.ExpectQuery(`(?s)SELECT p\.user_id::text, p\.notification_level, \(p\.muted_at IS NOT NULL\).*p\.`+test.column+` = \$2::uuid.*p\.user_id = ANY\(\$3::uuid\[\]\)`).
				WithArgs("ws-1", "target-1", []string{"user-1", "user-2"}).
				WillReturnRows(pgxmock.NewRows([]string{"user_id", "notification_level", "muted"}).
					AddRow("user-1", "mentions_replies", false).
					AddRow("user-2", "all", true))

			prefs, err := storage.NewPGXNotificationPrefStore(mock).PreferencesForUsers(
				context.Background(), "ws-1", test.targetType, "target-1", []string{"user-1", "user-2"})
			if err != nil {
				t.Fatalf("PreferencesForUsers: %v", err)
			}
			if len(prefs) != 2 ||
				prefs[0].Level != storage.NotificationLevelMentionsReplies || prefs[0].Muted ||
				prefs[1].Level != storage.NotificationLevelAll || !prefs[1].Muted {
				t.Fatalf("prefs = %+v, want a level-only and a muted recipient", prefs)
			}
			checkExpectations(t, mock)
		})
	}
}

// An empty recipient list issues no statement: a broadcast with no subscribers
// must not cost a query.
func TestPGXNotificationPrefStore_PreferencesForUsers_EmptyListIssuesNoQuery(t *testing.T) {
	mock := newMock(t)
	prefs, err := storage.NewPGXNotificationPrefStore(mock).PreferencesForUsers(
		context.Background(), "ws-1", storage.NotificationPrefTargetChannel, "chan-1", nil)
	if err != nil || prefs != nil {
		t.Fatalf("PreferencesForUsers = (%v, %v), want (nil, nil)", prefs, err)
	}
	checkExpectations(t, mock)
}

// An unrecognised target kind is an error and not an answer: reporting nobody
// as muted would alert people who asked not to be, and reporting everybody
// would silence people who did not ask for it.
func TestPGXNotificationPrefStore_PreferencesForUsers_RejectsAnUnknownTargetKind(t *testing.T) {
	mock := newMock(t)
	if _, err := storage.NewPGXNotificationPrefStore(mock).PreferencesForUsers(
		context.Background(), "ws-1", "workspace", "ws-1", []string{"user-1"},
	); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("error = %v, want ErrInvalidInput", err)
	}
	checkExpectations(t, mock)
}
