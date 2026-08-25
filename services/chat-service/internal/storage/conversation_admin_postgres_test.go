package storage_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

// Group rename, self-leave, and the general-channel invariant (issue #527),
// against a real PostgreSQL because every one of these properties is enforced in
// SQL rather than in Go.

const (
	adminGroupID       = "d1000000-0000-4000-8000-000000000001"
	adminDirectID      = "d1000000-0000-4000-8000-000000000002"
	adminLeaveChanID   = "c1000000-0000-4000-8000-000000000050"
	adminPrivateChanID = "c1000000-0000-4000-8000-000000000051"
)

// seedConversationAdminFixtures adds a group, a 1:1 conversation and two
// ordinary channels to the shared channel-authorization workspace.
//
// The 1:1 exists precisely so the "a DM can never be renamed or left" claim is
// tested against a real direct conversation rather than asserted.
func seedConversationAdminFixtures(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx := t.Context()
	for _, userID := range []string{chanOwner, chanAdmin, chanMember, chanGuest} {
		if _, err := pool.Exec(ctx, `
			INSERT INTO auth.users (id, email, display_name)
			VALUES ($1::uuid, $1::text || '@example.test', 'User')
			ON CONFLICT (id) DO NOTHING`, userID); err != nil {
			t.Fatalf("seed user: %v", err)
		}
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO chat.channels (id, workspace_id, slug, display_name, type, is_general, status)
		VALUES ($1, $3, 'saida', 'Saida', 'public', false, 'active'),
		       ($2, $3, 'privado-saida', 'Privado', 'private', false, 'active')`,
		adminLeaveChanID, adminPrivateChanID, chanWorkspace,
	); err != nil {
		t.Fatalf("seed leave channels: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO chat.channel_members (channel_id, user_id, role)
		VALUES ($1, $3, 'member'), ($2, $3, 'member'), ($1, $4, 'member')`,
		adminLeaveChanID, adminPrivateChanID, chanMember, chanOwner,
	); err != nil {
		t.Fatalf("seed channel members: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO chat.dm_conversations (id, workspace_id, type, title, status, created_by, direct_pair_key)
		VALUES ($1, $3, 'group', 'Equipe', 'active', $4, NULL),
		       ($2, $3, 'direct', NULL, 'active', $4, 'pair-owner-member')`,
		adminGroupID, adminDirectID, chanWorkspace, chanOwner,
	); err != nil {
		t.Fatalf("seed conversations: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO chat.dm_members (conversation_id, user_id, role, status)
		VALUES ($1, $3, 'member', 'active'), ($1, $4, 'member', 'active'),
		       ($2, $3, 'member', 'active'), ($2, $4, 'member', 'active')`,
		adminGroupID, adminDirectID, chanOwner, chanMember,
	); err != nil {
		t.Fatalf("seed dm members: %v", err)
	}
}

func groupTitle(t *testing.T, pool *pgxpool.Pool, conversationID string) string {
	t.Helper()
	var title string
	if err := pool.QueryRow(t.Context(),
		`SELECT COALESCE(title, '') FROM chat.dm_conversations WHERE id = $1`, conversationID,
	).Scan(&title); err != nil {
		t.Fatalf("read title: %v", err)
	}
	return title
}

// conversationEvents returns the system messages of one target, so a test can
// assert an event exists exactly when the change it describes does.
func conversationEvents(t *testing.T, pool *pgxpool.Pool, column, targetID string) []string {
	t.Helper()
	rows, err := pool.Query(t.Context(),
		`SELECT event_type FROM chat.messages
		 WHERE `+column+` = $1 AND kind = 'system' ORDER BY created_at`, targetID)
	if err != nil {
		t.Fatalf("read conversation events: %v", err)
	}
	defer rows.Close()
	events := []string{}
	for rows.Next() {
		var eventType string
		if err := rows.Scan(&eventType); err != nil {
			t.Fatalf("scan event: %v", err)
		}
		events = append(events, eventType)
	}
	return events
}

// ── Group rename ─────────────────────────────────────────────────────────────

// Participation is the whole authority: a group has no owner or admin, because
// chat.dm_members.role is CHECK-closed to 'member'.
func TestPGXDMStoreRenameGroupAuthorizationPostgreSQL(t *testing.T) {
	for _, test := range []struct {
		name     string
		callerID string
		wantErr  error
	}{
		{name: "participant", callerID: chanMember},
		{name: "other participant", callerID: chanOwner},
		// A workspace admin who is not in the group has no standing in it.
		{name: "workspace admin outside the group", callerID: chanAdmin, wantErr: domain.ErrForbidden},
		{name: "not a workspace member", callerID: chanStranger, wantErr: domain.ErrForbidden},
	} {
		t.Run(test.name, func(t *testing.T) {
			pool := newChannelAuthzPool(t)
			seedConversationAdminFixtures(t, pool)
			store := storage.NewPGXDMStore(pool)

			result, err := store.RenameGroupConversation(t.Context(), storage.RenameGroupInput{
				WorkspaceID:    chanWorkspace,
				ConversationID: adminGroupID,
				CallerID:       test.callerID,
				Title:          "Piloto NChat",
			})
			if test.wantErr != nil {
				if !errors.Is(err, test.wantErr) {
					t.Fatalf("error = %v, want %v", err, test.wantErr)
				}
				if got := groupTitle(t, pool, adminGroupID); got != "Equipe" {
					t.Fatalf("title = %q, want the original after a refusal", got)
				}
				if events := conversationEvents(t, pool, "dm_conversation_id", adminGroupID); len(events) != 0 {
					t.Fatalf("a refused rename wrote %v", events)
				}
				return
			}
			if err != nil {
				t.Fatalf("RenameGroupConversation: %v", err)
			}
			if result.Conversation.ID != adminGroupID {
				t.Fatalf("conversation id = %q, want it unchanged", result.Conversation.ID)
			}
			if got := groupTitle(t, pool, adminGroupID); got != "Piloto NChat" {
				t.Fatalf("title = %q, want the rename persisted", got)
			}
			// The event and the change are one transaction, and it carries the
			// structured before/after rather than a sentence.
			if events := conversationEvents(t, pool, "dm_conversation_id", adminGroupID); len(events) != 1 ||
				events[0] != string(domain.ConversationEventRenamed) {
				t.Fatalf("events = %v, want exactly one rename event", events)
			}
			if result.Event.EventPayload.OldName != "Equipe" || result.Event.EventPayload.NewName != "Piloto NChat" {
				t.Fatalf("event payload = %+v, want the old and new names", result.Event.EventPayload)
			}
			if result.Event.SenderID != test.callerID {
				t.Fatalf("event actor = %q, want the caller — never a client-supplied name", result.Event.SenderID)
			}
		})
	}
}

// Leaving ends the authority, and it ends it here rather than only in the UI:
// the participation statement requires an *active* dm_members row, and a
// departure marks that row left.
func TestPGXDMStoreRenameGroupRefusesAMemberWhoLeftPostgreSQL(t *testing.T) {
	pool := newChannelAuthzPool(t)
	seedConversationAdminFixtures(t, pool)
	store := storage.NewPGXDMStore(pool)

	if _, err := store.LeaveGroupConversation(t.Context(), chanWorkspace, adminGroupID, chanMember); err != nil {
		t.Fatalf("LeaveGroupConversation: %v", err)
	}
	if _, err := store.RenameGroupConversation(t.Context(), storage.RenameGroupInput{
		WorkspaceID:    chanWorkspace,
		ConversationID: adminGroupID,
		CallerID:       chanMember,
		Title:          "Piloto NChat",
	}); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("rename error = %v, want ErrForbidden for someone who left", err)
	}
	if got := groupTitle(t, pool, adminGroupID); got != "Equipe" {
		t.Fatalf("title = %q, want it untouched", got)
	}
	// Only the departure is in the history — the refused rename added nothing.
	if events := conversationEvents(t, pool, "dm_conversation_id", adminGroupID); len(events) != 1 ||
		events[0] != string(domain.ConversationEventMemberLeft) {
		t.Fatalf("events = %v, want only the departure", events)
	}
}

// A 1:1 conversation is not renameable, and not because a check refuses it: the
// statement requires type = 'group', so a direct conversation's ID reaches
// nothing at all.
func TestPGXDMStoreRenameGroupCannotTouchADirectConversationPostgreSQL(t *testing.T) {
	pool := newChannelAuthzPool(t)
	seedConversationAdminFixtures(t, pool)
	store := storage.NewPGXDMStore(pool)

	_, err := store.RenameGroupConversation(t.Context(), storage.RenameGroupInput{
		WorkspaceID:    chanWorkspace,
		ConversationID: adminDirectID,
		CallerID:       chanMember,
		Title:          "Apelido",
	})
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("error = %v, want ErrNotFound for a 1:1 conversation", err)
	}
	if got := groupTitle(t, pool, adminDirectID); got != "" {
		t.Fatalf("direct conversation title = %q, want it untouched", got)
	}
}

// ── Group self-leave ─────────────────────────────────────────────────────────

func TestPGXDMStoreLeaveGroupPostgreSQL(t *testing.T) {
	pool := newChannelAuthzPool(t)
	seedConversationAdminFixtures(t, pool)
	store := storage.NewPGXDMStore(pool)

	if _, err := store.LeaveGroupConversation(t.Context(), chanWorkspace, adminGroupID, chanMember); err != nil {
		t.Fatalf("LeaveGroupConversation: %v", err)
	}

	var status string
	if err := pool.QueryRow(t.Context(),
		`SELECT status FROM chat.dm_members WHERE conversation_id = $1 AND user_id = $2`,
		adminGroupID, chanMember,
	).Scan(&status); err != nil {
		t.Fatalf("read membership: %v", err)
	}
	if status != "left" {
		t.Fatalf("status = %q, want left", status)
	}
	// The history is preserved for whoever stays, and the departure is in it.
	if events := conversationEvents(t, pool, "dm_conversation_id", adminGroupID); len(events) != 1 ||
		events[0] != string(domain.ConversationEventMemberLeft) {
		t.Fatalf("events = %v, want exactly one member-left event", events)
	}
	// Leaving twice is refused rather than writing a second event.
	if _, err := store.LeaveGroupConversation(t.Context(), chanWorkspace, adminGroupID, chanMember); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("second leave error = %v, want ErrForbidden", err)
	}
	if events := conversationEvents(t, pool, "dm_conversation_id", adminGroupID); len(events) != 1 {
		t.Fatalf("events = %v, want the refused second leave to have written nothing", events)
	}
}

// A 1:1 conversation cannot be left, for the same structural reason it cannot be
// renamed: the statement only ever matches a group.
func TestPGXDMStoreLeaveGroupCannotTouchADirectConversationPostgreSQL(t *testing.T) {
	pool := newChannelAuthzPool(t)
	seedConversationAdminFixtures(t, pool)
	store := storage.NewPGXDMStore(pool)

	if _, err := store.LeaveGroupConversation(t.Context(), chanWorkspace, adminDirectID, chanMember); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("error = %v, want ErrNotFound for a 1:1 conversation", err)
	}
	var status string
	if err := pool.QueryRow(t.Context(),
		`SELECT status FROM chat.dm_members WHERE conversation_id = $1 AND user_id = $2`,
		adminDirectID, chanMember,
	).Scan(&status); err != nil {
		t.Fatalf("read membership: %v", err)
	}
	if status != "active" {
		t.Fatalf("direct membership status = %q, want it untouched", status)
	}
}

// ── Channel self-leave ───────────────────────────────────────────────────────

func TestPGXChannelStoreLeaveChannelSelfPostgreSQL(t *testing.T) {
	pool := newChannelAuthzPool(t)
	seedConversationAdminFixtures(t, pool)
	store := storage.NewPGXChannelStore(pool)

	if _, err := store.LeaveChannelSelf(t.Context(), chanWorkspace, adminPrivateChanID, chanMember); err != nil {
		t.Fatalf("LeaveChannelSelf: %v", err)
	}

	var remaining int
	if err := pool.QueryRow(t.Context(),
		`SELECT count(*) FROM chat.channel_members WHERE channel_id = $1 AND user_id = $2`,
		adminPrivateChanID, chanMember,
	).Scan(&remaining); err != nil {
		t.Fatalf("count membership: %v", err)
	}
	if remaining != 0 {
		t.Fatalf("membership rows = %d, want the actor's own removed", remaining)
	}
	if events := conversationEvents(t, pool, "channel_id", adminPrivateChanID); len(events) != 1 ||
		events[0] != string(domain.ConversationEventMemberLeft) {
		t.Fatalf("events = %v, want exactly one member-left event", events)
	}
	// The channel and its history survive: leaving removes a membership, never
	// content.
	var channelStatus string
	if err := pool.QueryRow(t.Context(),
		`SELECT status FROM chat.channels WHERE id = $1`, adminPrivateChanID,
	).Scan(&channelStatus); err != nil {
		t.Fatalf("read channel: %v", err)
	}
	if channelStatus != "active" {
		t.Fatalf("channel status = %q, want the channel untouched", channelStatus)
	}
}

// Only the actor's own membership is affected — another member's row survives.
func TestPGXChannelStoreLeaveChannelSelfTouchesOnlyTheActorPostgreSQL(t *testing.T) {
	pool := newChannelAuthzPool(t)
	seedConversationAdminFixtures(t, pool)
	store := storage.NewPGXChannelStore(pool)

	if _, err := store.LeaveChannelSelf(t.Context(), chanWorkspace, adminLeaveChanID, chanMember); err != nil {
		t.Fatalf("LeaveChannelSelf: %v", err)
	}
	var otherRemains int
	if err := pool.QueryRow(t.Context(),
		`SELECT count(*) FROM chat.channel_members WHERE channel_id = $1 AND user_id = $2`,
		adminLeaveChanID, chanOwner,
	).Scan(&otherRemains); err != nil {
		t.Fatalf("count other membership: %v", err)
	}
	if otherRemains != 1 {
		t.Fatalf("other member rows = %d, want them untouched", otherRemains)
	}
}

// ── The general channel ──────────────────────────────────────────────────────

// Structural, for every role, enforced in SQL. The UI hiding the action is not
// what makes this true.
func TestPGXChannelStoreGeneralChannelCannotBeLeftPostgreSQL(t *testing.T) {
	pool := newChannelAuthzPool(t)
	seedConversationAdminFixtures(t, pool)
	store := storage.NewPGXChannelStore(pool)

	for _, actor := range []struct {
		name   string
		userID string
	}{
		{name: "owner", userID: chanOwner},
		{name: "admin", userID: chanAdmin},
		{name: "member", userID: chanMember},
	} {
		t.Run(actor.name, func(t *testing.T) {
			_, err := store.LeaveChannelSelf(t.Context(), chanWorkspace, chanGeneral, actor.userID)
			if !errors.Is(err, domain.ErrGeneralChannelImmutable) {
				t.Fatalf("error = %v, want the structural refusal", err)
			}
			if events := conversationEvents(t, pool, "channel_id", chanGeneral); len(events) != 0 {
				t.Fatalf("a refused leave wrote %v", events)
			}
		})
	}
}

// Leaving a channel the actor never joined changes nothing and says so, rather
// than putting a departure in the history of a conversation nobody left.
func TestPGXChannelStoreLeaveChannelWithoutMembershipPostgreSQL(t *testing.T) {
	pool := newChannelAuthzPool(t)
	seedConversationAdminFixtures(t, pool)
	store := storage.NewPGXChannelStore(pool)

	if _, err := store.LeaveChannelSelf(t.Context(), chanWorkspace, adminLeaveChanID, chanGuest); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("error = %v, want ErrNotFound", err)
	}
	if events := conversationEvents(t, pool, "channel_id", adminLeaveChanID); len(events) != 0 {
		t.Fatalf("a no-op leave wrote %v", events)
	}
}

// A channel of another workspace is not reachable, so the route cannot be used
// to remove a membership elsewhere.
func TestPGXChannelStoreLeaveChannelIsWorkspaceBoundPostgreSQL(t *testing.T) {
	pool := newChannelAuthzPool(t)
	seedConversationAdminFixtures(t, pool)
	store := storage.NewPGXChannelStore(pool)

	if _, err := store.LeaveChannelSelf(t.Context(), chanOtherWorkspace, adminLeaveChanID, chanMember); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("error = %v, want ErrNotFound for a cross-workspace leave", err)
	}
	var remaining int
	if err := pool.QueryRow(t.Context(),
		`SELECT count(*) FROM chat.channel_members WHERE channel_id = $1 AND user_id = $2`,
		adminLeaveChanID, chanMember,
	).Scan(&remaining); err != nil {
		t.Fatalf("count membership: %v", err)
	}
	if remaining != 1 {
		t.Fatalf("membership rows = %d, want the membership untouched", remaining)
	}
}

// ── System messages are unforgeable ──────────────────────────────────────────

// The database refuses a user message carrying an event, and a system message
// without one. That pairing is what stops the ordinary send endpoint — which
// accepts neither column — from ever producing something that renders as a
// system event.
func TestChatMessagesRefuseForgedSystemEventsPostgreSQL(t *testing.T) {
	pool := newChannelAuthzPool(t)
	seedConversationAdminFixtures(t, pool)

	_, err := pool.Exec(t.Context(), `
		INSERT INTO chat.messages (workspace_id, channel_id, sender_id, kind, body_text, event_type, event_payload)
		VALUES ($1, $2, $3, 'user', 'olá', 'conversation_renamed', '{}'::jsonb)`,
		chanWorkspace, adminLeaveChanID, chanMember,
	)
	if err == nil {
		t.Fatal("a user message was allowed to carry a conversation event")
	}

	_, err = pool.Exec(t.Context(), `
		INSERT INTO chat.messages (workspace_id, channel_id, sender_id, kind, body_text)
		VALUES ($1, $2, $3, 'system', 'olá')`,
		chanWorkspace, adminLeaveChanID, chanMember,
	)
	if err == nil {
		t.Fatal("a system message was allowed without a structured event")
	}
}

// ── Channel rename writes its event in the same transaction (issue #527) ─────

func TestPGXChannelStoreRenameChannelWritesSystemMessagePostgreSQL(t *testing.T) {
	pool := newChannelAuthzPool(t)
	seedConversationAdminFixtures(t, pool)
	store := storage.NewPGXChannelStore(pool)

	result, err := store.UpdateChannel(t.Context(), storage.UpdateChannelInput{
		CallerID:    chanOwner,
		WorkspaceID: chanWorkspace,
		ChannelID:   adminLeaveChanID,
		Slug:        "saida",
		DisplayName: "Projetos Especiais",
		Type:        domain.ChannelTypePublic,
	})
	if err != nil {
		t.Fatalf("UpdateChannel: %v", err)
	}
	if got := channelDisplayName(t, pool, adminLeaveChanID); got != "Projetos Especiais" {
		t.Fatalf("display_name = %q, want the rename persisted", got)
	}

	// Exactly one system message, of the shared conversation_renamed type — the
	// same structural model the group rename uses, not a channel-only variant.
	if events := conversationEvents(t, pool, "channel_id", adminLeaveChanID); len(events) != 1 ||
		events[0] != string(domain.ConversationEventRenamed) {
		t.Fatalf("events = %v, want exactly one conversation_renamed", events)
	}
	if result.Event.Kind != domain.MessageKindSystem {
		t.Fatalf("event kind = %q, want system", result.Event.Kind)
	}
	if result.Event.SenderID != chanOwner {
		t.Fatalf("event actor = %q, want the caller — never a client-supplied name", result.Event.SenderID)
	}
	if result.Event.ChannelID != adminLeaveChanID {
		t.Fatalf("event channel = %q, want the renamed channel", result.Event.ChannelID)
	}
	if result.Event.EventPayload.OldName != "Saida" ||
		result.Event.EventPayload.NewName != "Projetos Especiais" {
		t.Fatalf("payload = %+v, want the old and new names", result.Event.EventPayload)
	}
	// The persisted payload is structured, never a pre-formatted sentence.
	var payload string
	if err := pool.QueryRow(t.Context(),
		`SELECT event_payload::text FROM chat.messages WHERE id = $1`, result.Event.ID,
	).Scan(&payload); err != nil {
		t.Fatalf("read payload: %v", err)
	}
	for _, absent := range []string{"renomeou", "renamed", chanOwner} {
		if strings.Contains(payload, absent) {
			t.Fatalf("payload %s contains %q — it must carry facts, not a sentence or an actor name", payload, absent)
		}
	}
}

// An update that changes no name is not a rename and must put nothing in the
// timeline.
func TestPGXChannelStoreUpdateWithoutRenameWritesNoEventPostgreSQL(t *testing.T) {
	pool := newChannelAuthzPool(t)
	seedConversationAdminFixtures(t, pool)
	store := storage.NewPGXChannelStore(pool)

	result, err := store.UpdateChannel(t.Context(), storage.UpdateChannelInput{
		CallerID:    chanOwner,
		WorkspaceID: chanWorkspace,
		ChannelID:   adminLeaveChanID,
		Slug:        "saida",
		DisplayName: "Saida",
		Type:        domain.ChannelTypePublic,
	})
	if err != nil {
		t.Fatalf("UpdateChannel: %v", err)
	}
	if result.Event.ID != "" {
		t.Fatalf("event = %+v, want none when the name did not change", result.Event)
	}
	if events := conversationEvents(t, pool, "channel_id", adminLeaveChanID); len(events) != 0 {
		t.Fatalf("events = %v, want none", events)
	}
}

// The general channel is refused, and a refusal writes nothing at all — no
// rename, no event.
func TestPGXChannelStoreGeneralChannelRenameWritesNoEventPostgreSQL(t *testing.T) {
	pool := newChannelAuthzPool(t)
	seedConversationAdminFixtures(t, pool)
	store := storage.NewPGXChannelStore(pool)

	for _, actor := range []struct {
		name   string
		userID string
	}{
		{name: "owner", userID: chanOwner},
		{name: "admin", userID: chanAdmin},
	} {
		t.Run(actor.name, func(t *testing.T) {
			_, err := store.UpdateChannel(t.Context(), storage.UpdateChannelInput{
				CallerID:    actor.userID,
				WorkspaceID: chanWorkspace,
				ChannelID:   chanGeneral,
				Slug:        "geral",
				DisplayName: "Renomeado",
				Type:        domain.ChannelTypePublic,
			})
			if !errors.Is(err, domain.ErrNotFound) {
				t.Fatalf("error = %v, want the refusal for #geral", err)
			}
			if got := channelDisplayName(t, pool, chanGeneral); got != "geral" {
				t.Fatalf("display_name = %q, want #geral untouched", got)
			}
			if events := conversationEvents(t, pool, "channel_id", chanGeneral); len(events) != 0 {
				t.Fatalf("a refused rename of #geral wrote %v", events)
			}
		})
	}
}

// ── System messages must not leak into the other message surfaces ────────────
//
// kind='system' rows are new in chat.messages (issue #527), so every surface
// that reads that table is a place they could show up wrongly. These pin the
// behaviour down against a real PostgreSQL rather than by reasoning about it.

func TestSystemMessageSideEffectsPostgreSQL(t *testing.T) {
	pool := newChannelAuthzPool(t)
	seedConversationAdminFixtures(t, pool)
	store := storage.NewPGXChannelStore(pool)

	result, err := store.UpdateChannel(t.Context(), storage.UpdateChannelInput{
		CallerID:    chanOwner,
		WorkspaceID: chanWorkspace,
		ChannelID:   adminLeaveChanID,
		Slug:        "saida",
		DisplayName: "Projetos Especiais",
		Type:        domain.ChannelTypePublic,
	})
	if err != nil {
		t.Fatalf("UpdateChannel: %v", err)
	}

	// Search: the vector is derived from body_text, which a system message
	// leaves empty, and the names live in event_payload, which is never indexed.
	// So a rename is not findable as if someone had typed it.
	t.Run("is not full-text searchable", func(t *testing.T) {
		var matches int
		if err := pool.QueryRow(t.Context(), `
			SELECT count(*) FROM chat.messages
			WHERE id = $1 AND search_vector @@ plainto_tsquery('portuguese', 'Projetos Especiais')`,
			result.Event.ID,
		).Scan(&matches); err != nil {
			t.Fatalf("search: %v", err)
		}
		if matches != 0 {
			t.Fatal("the renamed-to value became searchable as message content")
		}
	})

	// Mentions: the pipeline parses body_text for mention tokens, and a system
	// message has none — it carries facts in a jsonb column instead. So an event
	// can never produce a mention row, and never a mention badge.
	t.Run("creates no mention", func(t *testing.T) {
		var mentions int
		if err := pool.QueryRow(t.Context(),
			`SELECT count(*) FROM chat.message_pending_mentions WHERE message_id = $1`, result.Event.ID,
		).Scan(&mentions); err != nil {
			t.Fatalf("read pending mentions: %v", err)
		}
		if mentions != 0 {
			t.Fatalf("a system message produced %d mention(s)", mentions)
		}
	})

	// Reactions: a reaction is for what somebody said. The store's authorization
	// requires kind='user', so a system message cannot be reacted to even by a
	// caller who bypassed the UI.
	t.Run("cannot be reacted to", func(t *testing.T) {
		reactions := storage.NewPGXReactionStore(pool)
		if _, err := reactions.ToggleReaction(t.Context(), storage.ToggleReactionInput{
			WorkspaceID: chanWorkspace,
			MessageID:   result.Event.ID,
			UserID:      chanOwner,
			Emoji:       "👍",
		}); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("ToggleReaction error = %v, want a refusal for a system message", err)
		}
	})

	// The body really is empty, which is what keeps every "show the text of the
	// last message" surface from rendering a blank or an "undefined": there is
	// no text, and the renderer builds its sentence from the structured event.
	t.Run("carries no body text", func(t *testing.T) {
		var body string
		if err := pool.QueryRow(t.Context(),
			`SELECT body_text FROM chat.messages WHERE id = $1`, result.Event.ID,
		).Scan(&body); err != nil {
			t.Fatalf("read body: %v", err)
		}
		if body != "" {
			t.Fatalf("body_text = %q, want empty — the sentence is presentation", body)
		}
	})
}

// ── System messages are not interactive (issue #527, code review) ────────────
//
// A system message records something that happened. Every operation that
// presupposes a user message — editing, pinning, replying to, quoting — must
// refuse it, and must keep working for an ordinary message alongside it.

func TestSystemMessageIsNotInteractivePostgreSQL(t *testing.T) {
	pool := newChannelAuthzPool(t)
	seedConversationAdminFixtures(t, pool)
	channels := storage.NewPGXChannelStore(pool)
	messages := storage.NewPGXMessageStore(pool)

	renamed, err := channels.UpdateChannel(t.Context(), storage.UpdateChannelInput{
		CallerID:    chanOwner,
		WorkspaceID: chanWorkspace,
		ChannelID:   adminLeaveChanID,
		Slug:        "saida",
		DisplayName: "Projetos Especiais",
		Type:        domain.ChannelTypePublic,
	})
	if err != nil {
		t.Fatalf("UpdateChannel: %v", err)
	}
	systemID := renamed.Event.ID

	// An ordinary message by the same actor, so every refusal below is shown to
	// be about the *kind* and not about the actor, the channel or the workspace.
	//
	// Inserted directly rather than through CreateMessage: that path also binds
	// attachments, which live in file-service's schema and are not part of this
	// harness. What these assertions need is simply a persisted user message.
	var userMessageID string
	if err := pool.QueryRow(t.Context(), `
		INSERT INTO chat.messages (workspace_id, channel_id, sender_id, kind, body_text, body_format)
		VALUES ($1, $2, $3, 'user', 'mensagem comum', 'v3')
		RETURNING id::text`,
		chanWorkspace, adminLeaveChanID, chanOwner,
	).Scan(&userMessageID); err != nil {
		t.Fatalf("seed user message: %v", err)
	}

	t.Run("cannot be edited", func(t *testing.T) {
		_, err := messages.EditMessage(t.Context(), storage.EditMessageInput{
			WorkspaceID: chanWorkspace,
			MessageID:   systemID,
			EditorID:    chanOwner,
			Body:        "forjado",
			BodyFormat:  domain.MessageBodyFormatV3,
		})
		if !errors.Is(err, domain.ErrEditForbidden) {
			t.Fatalf("EditMessage error = %v, want ErrEditForbidden", err)
		}
		// Nothing about the event moved.
		var body, eventType, payload string
		if err := pool.QueryRow(t.Context(),
			`SELECT body_text, COALESCE(event_type, ''), event_payload::text
			 FROM chat.messages WHERE id = $1`, systemID,
		).Scan(&body, &eventType, &payload); err != nil {
			t.Fatalf("read event: %v", err)
		}
		if body != "" || eventType != string(domain.ConversationEventRenamed) ||
			!strings.Contains(payload, "Projetos Especiais") {
			t.Fatalf("the event changed: body=%q type=%q payload=%s", body, eventType, payload)
		}
	})

	t.Run("an ordinary message is still editable", func(t *testing.T) {
		edited, err := messages.EditMessage(t.Context(), storage.EditMessageInput{
			WorkspaceID: chanWorkspace,
			MessageID:   userMessageID,
			EditorID:    chanOwner,
			Body:        "mensagem editada",
			BodyFormat:  domain.MessageBodyFormatV3,
		})
		if err != nil {
			t.Fatalf("EditMessage on a user message: %v", err)
		}
		if edited.BodyText != "mensagem editada" {
			t.Fatalf("body = %q, want the edit applied", edited.BodyText)
		}
	})

	t.Run("cannot be pinned as a message", func(t *testing.T) {
		pins := storage.NewPGXPinStore(pool)
		if err := pins.AddPin(t.Context(), chanWorkspace, "channel", adminLeaveChanID, systemID, chanOwner); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("AddPin error = %v, want a refusal for a system message", err)
		}
		// The sidebar's conversation pin is a different thing and is untouched:
		// an ordinary message still pins.
		if err := pins.AddPin(t.Context(), chanWorkspace, "channel", adminLeaveChanID, userMessageID, chanOwner); err != nil {
			t.Fatalf("AddPin on a user message: %v", err)
		}
	})

	t.Run("cannot be a reply parent", func(t *testing.T) {
		err := messages.ValidateRefMessageInTarget(
			t.Context(), chanWorkspace, adminLeaveChanID, "", systemID, chanOwner,
		)
		if !errors.Is(err, domain.ErrInvalidMessageReference) {
			t.Fatalf("ValidateRefMessageInTarget error = %v, want ErrInvalidMessageReference", err)
		}
		// A user message is still a valid parent.
		if err := messages.ValidateRefMessageInTarget(
			t.Context(), chanWorkspace, adminLeaveChanID, "", userMessageID, chanOwner,
		); err != nil {
			t.Fatalf("ValidateRefMessageInTarget on a user message: %v", err)
		}
	})

	t.Run("cannot be quoted", func(t *testing.T) {
		resolved, err := messages.ResolveMessageReferences(
			t.Context(), chanWorkspace, chanOwner, []string{systemID, userMessageID},
		)
		if err != nil {
			t.Fatalf("ResolveMessageReferences: %v", err)
		}
		if _, ok := resolved[systemID]; ok {
			t.Fatal("a system message resolved as a quotable reference")
		}
		if _, ok := resolved[userMessageID]; !ok {
			t.Fatal("a user message stopped being quotable")
		}
	})

	t.Run("cannot be favorited", func(t *testing.T) {
		favorites := storage.NewPGXFavoriteStore(pool)
		err := favorites.AddFavorite(t.Context(), storage.AddFavoriteInput{
			WorkspaceID: chanWorkspace,
			UserID:      chanOwner,
			MessageID:   systemID,
		})
		// The same non-enumerating answer an unreadable message produces: a
		// system message is not a thing that can be bookmarked, and the caller
		// learns nothing else from the refusal.
		if !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("AddFavorite error = %v, want ErrNotFound for a system message", err)
		}
		var stored int
		if err := pool.QueryRow(t.Context(),
			`SELECT count(*) FROM chat.message_favorites WHERE message_id = $1`, systemID,
		).Scan(&stored); err != nil {
			t.Fatalf("count favorites: %v", err)
		}
		if stored != 0 {
			t.Fatalf("%d favorite row(s) persisted for a system message", stored)
		}

		// And an ordinary message still favorites and still lists.
		if err := favorites.AddFavorite(t.Context(), storage.AddFavoriteInput{
			WorkspaceID: chanWorkspace,
			UserID:      chanOwner,
			MessageID:   userMessageID,
		}); err != nil {
			t.Fatalf("AddFavorite on a user message: %v", err)
		}
		if ids := favoriteMessageIDs(t, favorites, chanOwner); len(ids) != 1 || ids[0] != userMessageID {
			t.Fatalf("favorites = %v, want exactly the user message", ids)
		}
	})

	// Defence in depth for a row this build can no longer create: a favourite
	// written before the guard existed, or by any other route into the table,
	// must not come back as an interactive item. Nothing is deleted — the
	// historical row stays and simply stops being served.
	t.Run("a pre-existing favorite of a system message is not listed", func(t *testing.T) {
		if _, err := pool.Exec(t.Context(),
			`INSERT INTO chat.message_favorites (user_id, message_id) VALUES ($1, $2)`,
			chanOwner, systemID,
		); err != nil {
			t.Fatalf("seed legacy favorite: %v", err)
		}
		ids := favoriteMessageIDs(t, storage.NewPGXFavoriteStore(pool), chanOwner)
		for _, id := range ids {
			if id == systemID {
				t.Fatal("a system message was listed as a favorite")
			}
		}
		if len(ids) != 1 || ids[0] != userMessageID {
			t.Fatalf("favorites = %v, want the user message and nothing else", ids)
		}
		var stillStored int
		if err := pool.QueryRow(t.Context(),
			`SELECT count(*) FROM chat.message_favorites WHERE message_id = $1`, systemID,
		).Scan(&stillStored); err != nil {
			t.Fatalf("count favorites: %v", err)
		}
		if stillStored != 1 {
			t.Fatalf("legacy favorite rows = %d, want the row left alone", stillStored)
		}
	})

	t.Run("no child message was persisted", func(t *testing.T) {
		var children int
		if err := pool.QueryRow(t.Context(),
			`SELECT count(*) FROM chat.messages WHERE parent_message_id = $1`, systemID,
		).Scan(&children); err != nil {
			t.Fatalf("count children: %v", err)
		}
		if children != 0 {
			t.Fatalf("%d message(s) hang off a system message", children)
		}
	})
}

// favoriteMessageIDs lists one user's favourites, newest first, as message ids.
func favoriteMessageIDs(t *testing.T, favorites *storage.PGXFavoriteStore, userID string) []string {
	t.Helper()
	page, err := favorites.ListFavorites(t.Context(), storage.ListFavoritesInput{
		WorkspaceID: chanWorkspace,
		UserID:      userID,
	})
	if err != nil {
		t.Fatalf("ListFavorites: %v", err)
	}
	ids := make([]string, 0, len(page.Favorites))
	for _, favorite := range page.Favorites {
		ids = append(ids, favorite.Message.ID)
	}
	return ids
}
