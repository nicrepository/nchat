package storage_test

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

// Group identity (issue #1026) against PostgreSQL: the column round-trips, a
// rename leaves it alone, a reset removes it, and the authority is the rename's
// — participation in the legacy model, EditMetadata once ownership is enabled.
//
// The TestGroupIdentityPostgreSQL_ prefix is what the chat-service suite of
// scripts/ci/go-integration-test.sh selects, so these run in CI on their own
// clean database.

// newGroupIdentityFixture is the whole precondition of this suite, built from
// scratch on every test: the channel-authorization workspace, the admin
// fixtures (a group "Equipe" with chanOwner and chanMember, and a 1:1), and an
// auth.users wide enough for the sidebar projection, which resolves names
// from full_name and avatar_url. The alignment is the one the membership and
// add-members suites already apply, so a database left by either satisfies
// all three.
func newGroupIdentityFixture(t *testing.T) (*pgxpool.Pool, *storage.PGXDMStore) {
	t.Helper()
	pool := newChannelAuthzPool(t)
	if _, err := pool.Exec(t.Context(), `
		ALTER TABLE auth.users
			ADD COLUMN IF NOT EXISTS full_name TEXT,
			ADD COLUMN IF NOT EXISTS avatar_url TEXT`); err != nil {
		t.Fatalf("align auth.users columns: %v", err)
	}
	seedConversationAdminFixtures(t, pool)
	return pool, storage.NewPGXDMStore(pool)
}

// groupAvatar reads the stored value; nil is Automático.
func groupAvatar(t *testing.T, pool *pgxpool.Pool, conversationID string) *string {
	t.Helper()
	var emoji *string
	if err := pool.QueryRow(t.Context(),
		`SELECT avatar_emoji FROM chat.dm_conversations WHERE id = $1`, conversationID,
	).Scan(&emoji); err != nil {
		t.Fatalf("read avatar_emoji: %v", err)
	}
	return emoji
}

// requireAutomatic asserts the column is NULL — removed, not blanked.
func requireAutomatic(t *testing.T, pool *pgxpool.Pool, conversationID string) {
	t.Helper()
	if got := groupAvatar(t, pool, conversationID); got != nil {
		t.Fatalf("avatar_emoji = %q, want NULL (Automático)", *got)
	}
}

// requireEmoji asserts the column holds exactly this sequence.
func requireEmoji(t *testing.T, pool *pgxpool.Pool, conversationID, want string) {
	t.Helper()
	if got := groupAvatar(t, pool, conversationID); got == nil || *got != want {
		t.Fatalf("avatar_emoji = %v, want %q byte for byte", got, want)
	}
}

func setAvatar(t *testing.T, store *storage.PGXDMStore, callerID, emoji string) error {
	t.Helper()
	return store.SetGroupAvatarEmoji(t.Context(), storage.SetGroupAvatarInput{
		WorkspaceID: chanWorkspace, ConversationID: adminGroupID, CallerID: callerID, AvatarEmoji: emoji,
	})
}

func renameAdminGroup(t *testing.T, store *storage.PGXDMStore, title string) {
	t.Helper()
	if _, err := store.RenameGroupConversation(t.Context(), storage.RenameGroupInput{
		WorkspaceID: chanWorkspace, ConversationID: adminGroupID, CallerID: chanOwner, Title: title,
	}); err != nil {
		t.Fatalf("rename: %v", err)
	}
}

// listedAvatars is the sidebar projection, by conversation id.
func listedAvatars(t *testing.T, store *storage.PGXDMStore, callerID string) map[string]string {
	t.Helper()
	listed, err := store.ListVisibleConversationsWithParticipantIDs(t.Context(), chanWorkspace, callerID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	avatars := make(map[string]string, len(listed))
	for _, conversation := range listed {
		avatars[conversation.ID] = conversation.AvatarEmoji
	}
	return avatars
}

// A group that existed before the column is Automático, and a 1:1 has no
// identity of its own in the sidebar projection.
func TestGroupIdentityPostgreSQL_ExistingConversationsAreAutomatic(t *testing.T) {
	pool, store := newGroupIdentityFixture(t)

	requireAutomatic(t, pool, adminGroupID)
	avatars := listedAvatars(t, store, chanMember)
	if avatars[adminGroupID] != "" || avatars[adminDirectID] != "" {
		t.Fatalf("listed avatars = %v, want none", avatars)
	}
}

func TestGroupIdentityPostgreSQL_EmojiRoundTripsAndSurvivesRename(t *testing.T) {
	pool, store := newGroupIdentityFixture(t)

	if err := setAvatar(t, store, chanMember, "👩‍💻"); err != nil {
		t.Fatalf("set: %v", err)
	}
	requireEmoji(t, pool, adminGroupID, "👩‍💻")

	renameAdminGroup(t, store, "Plataforma")
	requireEmoji(t, pool, adminGroupID, "👩‍💻")
	if got := groupTitle(t, pool, adminGroupID); got != "Plataforma" {
		t.Fatalf("title = %q, want the rename persisted", got)
	}
	if got := listedAvatars(t, store, chanMember)[adminGroupID]; got != "👩‍💻" {
		t.Fatalf("listed avatar = %q, want the emoji", got)
	}
}

// Back to Automático: the value is removed, and nothing is narrated — the
// identity is presentation, only the rename is history.
func TestGroupIdentityPostgreSQL_ResetRemovesTheEmojiWithoutAnEvent(t *testing.T) {
	pool, store := newGroupIdentityFixture(t)
	if err := setAvatar(t, store, chanMember, "🎉"); err != nil {
		t.Fatalf("set: %v", err)
	}

	if err := setAvatar(t, store, chanOwner, ""); err != nil {
		t.Fatalf("reset: %v", err)
	}
	requireAutomatic(t, pool, adminGroupID)
	if events := conversationEvents(t, pool, "dm_conversation_id", adminGroupID); len(events) != 0 {
		t.Fatalf("events = %v, want none for an identity change", events)
	}
}

func TestGroupIdentityPostgreSQL_CreateStoresOnlyAChosenEmoji(t *testing.T) {
	for _, emoji := range []string{"", "🚀"} {
		t.Run("emoji="+emoji, func(t *testing.T) {
			pool, store := newGroupIdentityFixture(t)

			conversation, err := store.CreateGroupConversation(t.Context(), storage.CreateGroupConversationInput{
				WorkspaceID: chanWorkspace, CreatedBy: chanOwner, Title: "Novo", AvatarEmoji: emoji,
				ParticipantUserIDs: []string{chanOwner, chanMember, chanAdmin},
			})
			if err != nil {
				t.Fatalf("create: %v", err)
			}
			if conversation.AvatarEmoji != emoji {
				t.Fatalf("returned avatar = %q, want %q", conversation.AvatarEmoji, emoji)
			}
			if emoji == "" {
				requireAutomatic(t, pool, conversation.ID)
				return
			}
			requireEmoji(t, pool, conversation.ID, emoji)
		})
	}
}

// Every refusal leaves the column untouched: a 1:1, another workspace and an
// unknown id are one indistinguishable ErrNotFound; a non-participant (even a
// workspace admin) and a missing actor are ErrForbidden.
func TestGroupIdentityPostgreSQL_RefusalsWriteNothing(t *testing.T) {
	for _, test := range []struct {
		name           string
		input          storage.SetGroupAvatarInput
		wantErr        error
		conversationID string
	}{
		{name: "workspace admin outside the group", wantErr: domain.ErrForbidden,
			input: storage.SetGroupAvatarInput{WorkspaceID: chanWorkspace, ConversationID: adminGroupID, CallerID: chanAdmin}},
		{name: "not a workspace member", wantErr: domain.ErrForbidden,
			input: storage.SetGroupAvatarInput{WorkspaceID: chanWorkspace, ConversationID: adminGroupID, CallerID: chanStranger}},
		{name: "no actor", wantErr: domain.ErrForbidden,
			input: storage.SetGroupAvatarInput{WorkspaceID: chanWorkspace, ConversationID: adminGroupID}},
		{name: "a 1:1 conversation", wantErr: domain.ErrNotFound,
			input: storage.SetGroupAvatarInput{WorkspaceID: chanWorkspace, ConversationID: adminDirectID, CallerID: chanMember}},
		{name: "another workspace", wantErr: domain.ErrNotFound,
			input: storage.SetGroupAvatarInput{WorkspaceID: chanOtherWorkspace, ConversationID: adminGroupID, CallerID: chanMember}},
		{name: "unknown id", wantErr: domain.ErrNotFound,
			input: storage.SetGroupAvatarInput{WorkspaceID: chanWorkspace, ConversationID: "d1000000-0000-4000-8000-0000000000ff", CallerID: chanMember}},
	} {
		t.Run(test.name, func(t *testing.T) {
			pool, store := newGroupIdentityFixture(t)
			test.input.AvatarEmoji = "🎉"

			if err := store.SetGroupAvatarEmoji(t.Context(), test.input); !errors.Is(err, test.wantErr) {
				t.Fatalf("error = %v, want %v", err, test.wantErr)
			}
			requireAutomatic(t, pool, adminGroupID)
			requireAutomatic(t, pool, adminDirectID)
		})
	}
}

// The column's CHECK is the structural backstop behind the service: a direct
// conversation can never hold one, an empty string is not a value (NULL is
// Automático), and a value cannot grow without bound.
func TestGroupIdentityPostgreSQL_ColumnConstraint(t *testing.T) {
	for _, test := range []struct {
		name, id, value string
	}{
		{name: "direct conversation", id: adminDirectID, value: "🎉"},
		{name: "empty string", id: adminGroupID, value: ""},
		{name: "oversized", id: adminGroupID, value: "🎉🎉🎉🎉🎉🎉🎉🎉🎉🎉🎉🎉🎉🎉🎉🎉🎉"},
	} {
		t.Run(test.name, func(t *testing.T) {
			pool, _ := newGroupIdentityFixture(t)

			_, err := pool.Exec(t.Context(), `UPDATE chat.dm_conversations SET avatar_emoji = $2 WHERE id = $1`, test.id, test.value)
			if err == nil {
				t.Fatal("the CHECK constraint admitted the value")
			}
		})
	}
}

// Once ownership is enabled the identity follows the rename's capability: an
// owner or admin may change it, a plain member may not. Ownership resets the
// auth schema, so this lives with the ownership suite (TestOwnership*), which
// the CI measures on its own database.
func TestOwnershipGroupAvatarCapabilityPostgreSQL(t *testing.T) {
	pool := ownershipPool(t)
	enableOwnership(t, pool)
	store := storage.NewPGXDMStore(pool)
	set := func(actor string) error {
		return store.SetGroupAvatarEmoji(t.Context(), storage.SetGroupAvatarInput{
			WorkspaceID: ownershipWS, ConversationID: ownershipDM, CallerID: actor, AvatarEmoji: "🎉",
		})
	}

	ownershipExec(t, pool, `SELECT chat.assign_ownership('dm',$1,$2,'owner',$2,'manual')`, ownershipDM, ownershipA)
	ownershipExec(t, pool, `SELECT chat.assign_ownership('dm',$1,$2,'member',$3,'manual')`, ownershipDM, ownershipB, ownershipA)
	if err := set(ownershipB); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("member: error = %v, want ErrForbidden", err)
	}
	requireAutomatic(t, pool, ownershipDM)
	if err := set(ownershipA); err != nil {
		t.Fatalf("owner: %v", err)
	}
	requireEmoji(t, pool, ownershipDM, "🎉")
}
