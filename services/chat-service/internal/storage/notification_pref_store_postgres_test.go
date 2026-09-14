package storage_test

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

// Mute is a per-user preference, and the general channel is not silenceable
// (issue #527). Both properties are enforced in SQL, so both are proved here
// against a real PostgreSQL rather than against a fake.

const (
	muteChannelID        = "c1000000-0000-4000-8000-000000000040"
	mutePrivateChannelID = "c1000000-0000-4000-8000-000000000041"
	muteGroupID          = "c1000000-0000-4000-8000-000000000042"
	muteOtherGroupID     = "c1000000-0000-4000-8000-000000000043"
)

func seedMuteChannel(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	if _, err := pool.Exec(t.Context(), `
		INSERT INTO chat.channels (id, workspace_id, slug, display_name, type, is_general, status)
		VALUES ($1, $2, 'infra', 'Infra', 'public', false, 'active')`,
		muteChannelID, chanWorkspace,
	); err != nil {
		t.Fatalf("seed mute channel: %v", err)
	}
}

// seedMutePrivateChannel adds a private channel chanMember is a member of and
// chanOwner is not, so the visibility predicate has something to refuse.
func seedMutePrivateChannel(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	if _, err := pool.Exec(t.Context(), `
		INSERT INTO chat.channels (id, workspace_id, slug, display_name, type, is_general, status)
		VALUES ($1, $2, 'infra-privado', 'Infra Privado', 'private', false, 'active')`,
		mutePrivateChannelID, chanWorkspace,
	); err != nil {
		t.Fatalf("seed private channel: %v", err)
	}
	// chat.channel_members has no status column: membership of a channel is the
	// row existing, which is what chat.channel_visible_to_user tests.
	if _, err := pool.Exec(t.Context(), `
		INSERT INTO chat.channel_members (channel_id, user_id, role)
		VALUES ($1, $2, 'member')`,
		mutePrivateChannelID, chanMember,
	); err != nil {
		t.Fatalf("seed private channel membership: %v", err)
	}
}

// seedMuteGroups adds one group chanMember participates in and one they do not,
// so participation has something to refuse too. The second lives in the other
// workspace as well, which is what makes it the cross-tenant case.
func seedMuteGroups(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	if _, err := pool.Exec(t.Context(), `
		INSERT INTO chat.dm_conversations (id, workspace_id, type, title, status, created_by, direct_pair_key)
		VALUES ($1, $3, 'group', 'Squad', 'active', $5, NULL),
		       ($2, $4, 'group', 'Outro Squad', 'active', $6, NULL)`,
		muteGroupID, muteOtherGroupID, chanWorkspace, chanOtherWorkspace, chanOwner, chanForeignOwner,
	); err != nil {
		t.Fatalf("seed groups: %v", err)
	}
	if _, err := pool.Exec(t.Context(), `
		INSERT INTO chat.dm_members (conversation_id, user_id, role, status)
		VALUES ($1, $2, 'member', 'active'), ($1, $3, 'member', 'active'),
		       ($4, $5, 'member', 'active')`,
		muteGroupID, chanMember, chanOwner, muteOtherGroupID, chanForeignOwner,
	); err != nil {
		t.Fatalf("seed group members: %v", err)
	}
}

// mutedTargetIDs is the muted subset of one user's preferences, which since
// issue #136 is a filter over the listing rather than the listing itself: a row
// can now exist without being a mute.
func mutedTargetIDs(t *testing.T, store *storage.PGXNotificationPrefStore, userID string) []string {
	t.Helper()
	ids := make([]string, 0)
	for _, item := range listedPrefs(t, store, userID) {
		if item.Muted {
			ids = append(ids, item.TargetType+":"+item.TargetID)
		}
	}
	return ids
}

func listedPrefs(
	t *testing.T, store *storage.PGXNotificationPrefStore, userID string,
) []storage.ConversationNotificationPref {
	t.Helper()
	items, err := store.ListPreferences(t.Context(), chanWorkspace, userID)
	if err != nil {
		t.Fatalf("ListPreferences: %v", err)
	}
	return items
}

// prefFor is the one preference the listing holds for a target, and whether it
// holds one at all — the distinction the sparse representation rests on.
func prefFor(
	t *testing.T, store *storage.PGXNotificationPrefStore, userID, targetType, targetID string,
) (storage.ConversationNotificationPref, bool) {
	t.Helper()
	for _, item := range listedPrefs(t, store, userID) {
		if item.TargetType == targetType && item.TargetID == targetID {
			return item, true
		}
	}
	return storage.ConversationNotificationPref{}, false
}

// storedRows counts the rows one user actually has for a target, read straight
// from the table rather than through the visibility-filtered listing: the
// sparse-representation assertions are about what is persisted.
func storedRows(t *testing.T, pool *pgxpool.Pool, userID, channelID string) int {
	t.Helper()
	var total int
	if err := pool.QueryRow(t.Context(),
		`SELECT count(*) FROM chat.conversation_notification_prefs WHERE user_id = $1 AND channel_id = $2`,
		userID, channelID,
	).Scan(&total); err != nil {
		t.Fatalf("count preference rows: %v", err)
	}
	return total
}

// The headline invariant: one member silencing a channel changes nothing for
// anyone else, because the row is keyed by user.
func TestPGXNotificationPrefStoreMuteIsPerUserPostgreSQL(t *testing.T) {
	pool := newChannelAuthzPool(t)
	seedMuteChannel(t, pool)
	store := storage.NewPGXNotificationPrefStore(pool)

	if err := store.Mute(t.Context(), chanWorkspace, chanMember, storage.NotificationPrefTargetChannel, muteChannelID); err != nil {
		t.Fatalf("Mute: %v", err)
	}

	if got := mutedTargetIDs(t, store, chanMember); len(got) != 1 || got[0] != "channel:"+muteChannelID {
		t.Fatalf("muted for the actor = %v, want the one channel", got)
	}
	if got := mutedTargetIDs(t, store, chanOwner); len(got) != 0 {
		t.Fatalf("muted for another user = %v, want none — mute is individual", got)
	}
}

// Muting twice is one row, and unmuting is idempotent too: the UI toggles, and
// a repeated toggle must not become an error or a duplicate.
func TestPGXNotificationPrefStoreMuteIsIdempotentPostgreSQL(t *testing.T) {
	pool := newChannelAuthzPool(t)
	seedMuteChannel(t, pool)
	store := storage.NewPGXNotificationPrefStore(pool)

	for range 2 {
		if err := store.Mute(t.Context(), chanWorkspace, chanMember, storage.NotificationPrefTargetChannel, muteChannelID); err != nil {
			t.Fatalf("Mute: %v", err)
		}
	}
	if got := mutedTargetIDs(t, store, chanMember); len(got) != 1 {
		t.Fatalf("muted = %v, want exactly one row after two mutes", got)
	}

	for range 2 {
		if err := store.Unmute(t.Context(), chanMember, storage.NotificationPrefTargetChannel, muteChannelID); err != nil {
			t.Fatalf("Unmute: %v", err)
		}
	}
	if got := mutedTargetIDs(t, store, chanMember); len(got) != 0 {
		t.Fatalf("muted = %v, want none after unmuting", got)
	}
}

// The general channel is where everyone is reachable by construction. The
// refusal is in SQL, so it holds for every role and for a caller that reached
// storage without passing the service.
func TestPGXNotificationPrefStoreRefusesGeneralChannelPostgreSQL(t *testing.T) {
	pool := newChannelAuthzPool(t)
	store := storage.NewPGXNotificationPrefStore(pool)

	for _, actor := range []struct {
		name   string
		userID string
	}{
		{name: "owner", userID: chanOwner},
		{name: "admin", userID: chanAdmin},
		{name: "member", userID: chanMember},
	} {
		t.Run(actor.name, func(t *testing.T) {
			err := store.Mute(t.Context(), chanWorkspace, actor.userID, storage.NotificationPrefTargetChannel, chanGeneral)
			if !errors.Is(err, domain.ErrNotFound) {
				t.Fatalf("error = %v, want a refusal for #geral", err)
			}
			if got := mutedTargetIDs(t, store, actor.userID); len(got) != 0 {
				t.Fatalf("muted = %v, want #geral never silenced", got)
			}
		})
	}
}

// Visibility is re-checked in the write, so a stale client list cannot mute a
// channel the caller cannot see, and an arbitrary UUID mutes nothing.
func TestPGXNotificationPrefStoreRefusesInvisibleTargetsPostgreSQL(t *testing.T) {
	pool := newChannelAuthzPool(t)
	seedMuteChannel(t, pool)
	store := storage.NewPGXNotificationPrefStore(pool)

	for _, test := range []struct {
		name      string
		userID    string
		channelID string
	}{
		{name: "not a workspace member", userID: chanStranger, channelID: muteChannelID},
		{name: "channel that does not exist", userID: chanMember, channelID: "c1000000-0000-4000-8000-0000000000fe"},
		{name: "channel of another workspace", userID: chanMember, channelID: chanOtherGeneral},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := store.Mute(t.Context(), chanWorkspace, test.userID, storage.NotificationPrefTargetChannel, test.channelID); !errors.Is(err, domain.ErrNotFound) {
				t.Fatalf("error = %v, want ErrNotFound", err)
			}
		})
	}
}

// An unknown target kind is refused before any SQL runs: the two kinds are a
// closed set and a third one is not a shape this domain has.
func TestPGXNotificationPrefStoreRejectsUnknownTargetKindPostgreSQL(t *testing.T) {
	pool := newChannelAuthzPool(t)
	store := storage.NewPGXNotificationPrefStore(pool)

	if err := store.Mute(t.Context(), chanWorkspace, chanMember, "workspace", muteChannelID); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("Mute error = %v, want ErrInvalidInput", err)
	}
	if err := store.Unmute(t.Context(), chanMember, "workspace", muteChannelID); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("Unmute error = %v, want ErrInvalidInput", err)
	}
}

// Issue #136: the level is a second, independent dimension, and every one of
// these is about the two of them not overwriting each other.

// A row written before 000050 is a mute and nothing else. After the migration
// it carries the default level, so it still reads as silenced — the
// compatibility requirement the whole migration turns on.
func TestPGXNotificationPrefStoreLegacyMuteRowStaysMutedPostgreSQL(t *testing.T) {
	pool := newChannelAuthzPool(t)
	seedMuteChannel(t, pool)
	store := storage.NewPGXNotificationPrefStore(pool)

	// Exactly what the old code wrote: no level named at all, so the column
	// default is what a pre-migration row ends up with.
	if _, err := pool.Exec(t.Context(), `
		INSERT INTO chat.conversation_notification_prefs (user_id, workspace_id, channel_id)
		VALUES ($1, $2, $3)`, chanMember, chanWorkspace, muteChannelID,
	); err != nil {
		t.Fatalf("seed legacy mute row: %v", err)
	}

	pref, found := prefFor(t, store, chanMember, storage.NotificationPrefTargetChannel, muteChannelID)
	if !found || !pref.Muted {
		t.Fatalf("legacy row = %+v (found %v), want it still silenced", pref, found)
	}
	if pref.Level != storage.NotificationLevelAll {
		t.Fatalf("legacy level = %q, want the default", pref.Level)
	}
}

// The sparse representation, in both directions: the pure default is no row,
// and a non-default level is a row that must exist even though nothing is
// silenced.
func TestPGXNotificationPrefStoreLevelRowsAreSparsePostgreSQL(t *testing.T) {
	pool := newChannelAuthzPool(t)
	seedMuteChannel(t, pool)
	store := storage.NewPGXNotificationPrefStore(pool)
	ctx := t.Context()

	if err := store.SetLevel(ctx, chanWorkspace, chanMember,
		storage.NotificationPrefTargetChannel, muteChannelID, storage.NotificationLevelMentionsReplies); err != nil {
		t.Fatalf("SetLevel(mentions_replies): %v", err)
	}
	pref, found := prefFor(t, store, chanMember, storage.NotificationPrefTargetChannel, muteChannelID)
	if !found || pref.Level != storage.NotificationLevelMentionsReplies || pref.Muted {
		t.Fatalf("pref = %+v (found %v), want mentions_replies and not muted", pref, found)
	}

	if err := store.SetLevel(ctx, chanWorkspace, chanMember,
		storage.NotificationPrefTargetChannel, muteChannelID, storage.NotificationLevelAll); err != nil {
		t.Fatalf("SetLevel(all): %v", err)
	}
	if rows := storedRows(t, pool, chanMember, muteChannelID); rows != 0 {
		t.Fatalf("%d rows persisted for the pure default, want none", rows)
	}
}

// The invariant the issue is named for: silencing from the sidebar and turning
// notifications back on restores the level that was chosen, because neither
// operation ever writes the other's column.
func TestPGXNotificationPrefStoreMuteRoundTripPreservesTheLevelPostgreSQL(t *testing.T) {
	pool := newChannelAuthzPool(t)
	seedMuteChannel(t, pool)
	store := storage.NewPGXNotificationPrefStore(pool)
	ctx := t.Context()

	if err := store.SetLevel(ctx, chanWorkspace, chanMember,
		storage.NotificationPrefTargetChannel, muteChannelID, storage.NotificationLevelMentionsReplies); err != nil {
		t.Fatalf("SetLevel: %v", err)
	}
	if err := store.Mute(ctx, chanWorkspace, chanMember, storage.NotificationPrefTargetChannel, muteChannelID); err != nil {
		t.Fatalf("Mute: %v", err)
	}
	pref, _ := prefFor(t, store, chanMember, storage.NotificationPrefTargetChannel, muteChannelID)
	if !pref.Muted || pref.Level != storage.NotificationLevelMentionsReplies {
		t.Fatalf("after mute = %+v, want silenced with the level intact", pref)
	}

	if err := store.Unmute(ctx, chanMember, storage.NotificationPrefTargetChannel, muteChannelID); err != nil {
		t.Fatalf("Unmute: %v", err)
	}
	pref, found := prefFor(t, store, chanMember, storage.NotificationPrefTargetChannel, muteChannelID)
	if !found || pref.Muted || pref.Level != storage.NotificationLevelMentionsReplies {
		t.Fatalf("after unmute = %+v (found %v), want the level restored and nothing silenced", pref, found)
	}
}

// Starting from the default instead: silencing and restoring must land back on
// the default, and must not leave a row behind saying nothing.
func TestPGXNotificationPrefStoreMuteRoundTripFromDefaultPostgreSQL(t *testing.T) {
	pool := newChannelAuthzPool(t)
	seedMuteChannel(t, pool)
	store := storage.NewPGXNotificationPrefStore(pool)
	ctx := t.Context()

	if err := store.Mute(ctx, chanWorkspace, chanMember, storage.NotificationPrefTargetChannel, muteChannelID); err != nil {
		t.Fatalf("Mute: %v", err)
	}
	if err := store.Unmute(ctx, chanMember, storage.NotificationPrefTargetChannel, muteChannelID); err != nil {
		t.Fatalf("Unmute: %v", err)
	}
	if rows := storedRows(t, pool, chanMember, muteChannelID); rows != 0 {
		t.Fatalf("%d rows left after a mute round trip from the default, want none", rows)
	}
}

// Choosing a level is choosing to hear something, so it lifts a mute. Without
// this, selecting "mentions and replies" on a silenced conversation would leave
// it silent and the setting would appear to do nothing.
func TestPGXNotificationPrefStoreSetLevelClearsTheMutePostgreSQL(t *testing.T) {
	pool := newChannelAuthzPool(t)
	seedMuteChannel(t, pool)
	store := storage.NewPGXNotificationPrefStore(pool)
	ctx := t.Context()

	if err := store.Mute(ctx, chanWorkspace, chanMember, storage.NotificationPrefTargetChannel, muteChannelID); err != nil {
		t.Fatalf("Mute: %v", err)
	}
	if err := store.SetLevel(ctx, chanWorkspace, chanMember,
		storage.NotificationPrefTargetChannel, muteChannelID, storage.NotificationLevelMentionsReplies); err != nil {
		t.Fatalf("SetLevel: %v", err)
	}
	pref, _ := prefFor(t, store, chanMember, storage.NotificationPrefTargetChannel, muteChannelID)
	if pref.Muted || pref.Level != storage.NotificationLevelMentionsReplies {
		t.Fatalf("pref = %+v, want mentions_replies and unsilenced", pref)
	}
}

// A level outside the closed set never reaches the database, and the CHECK
// constraint is there in case anything ever does.
func TestPGXNotificationPrefStoreRejectsAnUnknownLevelPostgreSQL(t *testing.T) {
	pool := newChannelAuthzPool(t)
	seedMuteChannel(t, pool)
	store := storage.NewPGXNotificationPrefStore(pool)

	if err := store.SetLevel(t.Context(), chanWorkspace, chanMember,
		storage.NotificationPrefTargetChannel, muteChannelID, "everything_always"); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("SetLevel error = %v, want ErrInvalidInput", err)
	}
	if _, err := pool.Exec(t.Context(), `
		INSERT INTO chat.conversation_notification_prefs (user_id, workspace_id, channel_id, notification_level)
		VALUES ($1, $2, $3, 'everything_always')`, chanMember, chanWorkspace, muteChannelID,
	); err == nil {
		t.Fatal("the database accepted a level outside the CHECK constraint")
	}
}

// #geral: silence is refused and a level is not (issue #136, option A).
//
// The two halves are one test because the distinction is the whole product
// decision: the general channel is where everyone stays reachable by name, and
// mentions-and-replies keeps exactly that while silence would not.
func TestPGXNotificationPrefStoreGeneralChannelAcceptsALevelButNotAMutePostgreSQL(t *testing.T) {
	pool := newChannelAuthzPool(t)
	store := storage.NewPGXNotificationPrefStore(pool)
	ctx := t.Context()

	if err := store.SetLevel(ctx, chanWorkspace, chanMember,
		storage.NotificationPrefTargetChannel, chanGeneral, storage.NotificationLevelMentionsReplies); err != nil {
		t.Fatalf("SetLevel on #geral: %v", err)
	}
	pref, found := prefFor(t, store, chanMember, storage.NotificationPrefTargetChannel, chanGeneral)
	if !found || pref.Level != storage.NotificationLevelMentionsReplies || pref.Muted {
		t.Fatalf("#geral pref = %+v (found %v), want mentions_replies and unsilenced", pref, found)
	}

	if err := store.Mute(ctx, chanWorkspace, chanMember,
		storage.NotificationPrefTargetChannel, chanGeneral); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("Mute on #geral = %v, want a refusal", err)
	}
	// And the refusal left the level alone rather than half-applying.
	pref, _ = prefFor(t, store, chanMember, storage.NotificationPrefTargetChannel, chanGeneral)
	if pref.Muted || pref.Level != storage.NotificationLevelMentionsReplies {
		t.Fatalf("#geral pref after the refused mute = %+v, want it untouched", pref)
	}
	// Returning to the default is allowed, and leaves no row.
	if err := store.SetLevel(ctx, chanWorkspace, chanMember,
		storage.NotificationPrefTargetChannel, chanGeneral, storage.NotificationLevelAll); err != nil {
		t.Fatalf("SetLevel(all) on #geral: %v", err)
	}
	if rows := storedRows(t, pool, chanMember, chanGeneral); rows != 0 {
		t.Fatalf("%d rows left for #geral at the default, want none", rows)
	}
}

// The level write is authorised by the same predicates the mute is: a private
// channel the caller cannot see, a group they do not participate in, and a
// target in another tenant all produce no row and the same non-enumerating
// refusal.
func TestPGXNotificationPrefStoreSetLevelAuthorizationPostgreSQL(t *testing.T) {
	pool := newChannelAuthzPool(t)
	seedMuteChannel(t, pool)
	seedMutePrivateChannel(t, pool)
	seedMuteGroups(t, pool)
	store := storage.NewPGXNotificationPrefStore(pool)

	for _, test := range []struct {
		name       string
		userID     string
		targetType string
		targetID   string
		wantErr    error
	}{
		{name: "public channel, member", userID: chanMember,
			targetType: storage.NotificationPrefTargetChannel, targetID: muteChannelID},
		{name: "private channel, member of it", userID: chanMember,
			targetType: storage.NotificationPrefTargetChannel, targetID: mutePrivateChannelID},
		// An owner of the workspace who is not in the private channel. Role is
		// not access: this is the BOLA case the predicate exists for.
		{name: "private channel, workspace owner who is not a member", userID: chanOwner,
			targetType: storage.NotificationPrefTargetChannel, targetID: mutePrivateChannelID,
			wantErr: domain.ErrNotFound},
		{name: "group, participant", userID: chanMember,
			targetType: storage.NotificationPrefTargetDM, targetID: muteGroupID},
		{name: "group, not a participant", userID: chanAdmin,
			targetType: storage.NotificationPrefTargetDM, targetID: muteGroupID,
			wantErr: domain.ErrNotFound},
		{name: "group in another workspace", userID: chanMember,
			targetType: storage.NotificationPrefTargetDM, targetID: muteOtherGroupID,
			wantErr: domain.ErrNotFound},
		{name: "channel in another workspace", userID: chanMember,
			targetType: storage.NotificationPrefTargetChannel, targetID: chanOtherGeneral,
			wantErr: domain.ErrNotFound},
		{name: "no workspace membership at all", userID: chanStranger,
			targetType: storage.NotificationPrefTargetChannel, targetID: muteChannelID,
			wantErr: domain.ErrNotFound},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := store.SetLevel(t.Context(), chanWorkspace, test.userID,
				test.targetType, test.targetID, storage.NotificationLevelMentionsReplies)
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("SetLevel error = %v, want %v", err, test.wantErr)
			}
			_, found := prefFor(t, store, test.userID, test.targetType, test.targetID)
			if test.wantErr != nil && found {
				t.Fatal("a refused write left a preference behind")
			}
			if test.wantErr == nil && !found {
				t.Fatal("an allowed write persisted nothing")
			}
		})
	}
}

// The fan-out's read, which is the same rows asked the other way round: one
// target, many recipients, both halves of each preference, one statement.
func TestPGXNotificationPrefStorePreferencesForUsersPostgreSQL(t *testing.T) {
	pool := newChannelAuthzPool(t)
	seedMuteChannel(t, pool)
	store := storage.NewPGXNotificationPrefStore(pool)
	ctx := t.Context()

	if err := store.Mute(ctx, chanWorkspace, chanMember, storage.NotificationPrefTargetChannel, muteChannelID); err != nil {
		t.Fatalf("Mute: %v", err)
	}
	if err := store.SetLevel(ctx, chanWorkspace, chanOwner,
		storage.NotificationPrefTargetChannel, muteChannelID, storage.NotificationLevelMentionsReplies); err != nil {
		t.Fatalf("SetLevel: %v", err)
	}

	prefs, err := store.PreferencesForUsers(ctx, chanWorkspace,
		storage.NotificationPrefTargetChannel, muteChannelID,
		[]string{chanMember, chanOwner, chanAdmin})
	if err != nil {
		t.Fatalf("PreferencesForUsers: %v", err)
	}
	byUser := map[string]storage.UserConversationNotificationPref{}
	for _, pref := range prefs {
		byUser[pref.UserID] = pref
	}
	if got := byUser[chanMember]; !got.Muted || got.Level != storage.NotificationLevelAll {
		t.Fatalf("the muted recipient = %+v, want silenced at the default level", got)
	}
	if got := byUser[chanOwner]; got.Muted || got.Level != storage.NotificationLevelMentionsReplies {
		t.Fatalf("the level-only recipient = %+v, want mentions_replies and unsilenced", got)
	}
	if _, present := byUser[chanAdmin]; present {
		t.Fatalf("a recipient who expressed nothing was reported: %+v", byUser[chanAdmin])
	}
}

// Cross-workspace isolation on the read side: a preference row naming another
// tenant's workspace cannot be read back through this workspace, which is the
// predicate the prefs table's own foreign keys do not provide.
func TestPGXNotificationPrefStorePreferencesForUsersIsWorkspaceScopedPostgreSQL(t *testing.T) {
	pool := newChannelAuthzPool(t)
	seedMuteChannel(t, pool)
	store := storage.NewPGXNotificationPrefStore(pool)
	ctx := t.Context()

	if err := store.Mute(ctx, chanWorkspace, chanMember, storage.NotificationPrefTargetChannel, muteChannelID); err != nil {
		t.Fatalf("Mute: %v", err)
	}
	prefs, err := store.PreferencesForUsers(ctx, chanOtherWorkspace,
		storage.NotificationPrefTargetChannel, muteChannelID, []string{chanMember})
	if err != nil {
		t.Fatalf("PreferencesForUsers: %v", err)
	}
	if len(prefs) != 0 {
		t.Fatalf("read %d preferences through another workspace, want none", len(prefs))
	}
}
