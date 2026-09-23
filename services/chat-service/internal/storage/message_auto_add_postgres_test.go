package storage_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

func countAutoAddEvents(t *testing.T, pool *pgxpool.Pool, ctx context.Context, channelID, conversationID string) int {
	t.Helper()
	var total int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM chat.messages
		WHERE event_type = 'conversation_member_added'
		  AND channel_id IS NOT DISTINCT FROM $1::uuid
		  AND dm_conversation_id IS NOT DISTINCT FROM $2::uuid`,
		nullableAutoAddUUID(channelID), nullableAutoAddUUID(conversationID),
	).Scan(&total); err != nil {
		t.Fatalf("count membership events: %v", err)
	}
	return total
}

func nullableAutoAddUUID(id string) any {
	if id == "" {
		return nil
	}
	return id
}

func TestPGXCreateMessageAutoAddsMentionedChannelMemberAtomicallyPostgreSQL(t *testing.T) {
	pool, ctx := addMembersPostgres(t)
	if _, err := pool.Exec(ctx, `
		INSERT INTO chat.channel_members (channel_id, user_id, role)
		VALUES ($1, $2, 'member')`, amPrivate, amAdmin); err != nil {
		t.Fatalf("seed sender channel membership: %v", err)
	}
	store := storage.NewPGXMessageStore(pool)

	_, err := store.CreateMessage(ctx, storage.CreateMessageInput{
		WorkspaceID: amWS, ChannelID: amPrivate, SenderID: amAdmin,
		BodyText: "mention", BodyFormat: domain.MessageBodyFormatV3,
		ParentMessageID:  "f1000000-0000-4000-8000-000000000099",
		MentionedUserIDs: []string{amActive1},
	})
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("invalid message reference err = %v, want ErrNotFound", err)
	}
	if got := countChannelMembers(t, pool, ctx, amPrivate); got != 1 {
		t.Fatalf("failed message left %d memberships, want sender only", got)
	}
	if got := countAutoAddEvents(t, pool, ctx, amPrivate, ""); got != 0 {
		t.Fatalf("failed message left %d membership events, want none", got)
	}

	message, err := store.CreateMessage(ctx, storage.CreateMessageInput{
		WorkspaceID: amWS, ChannelID: amPrivate, SenderID: amAdmin,
		BodyText: "mention", BodyFormat: domain.MessageBodyFormatV3,
		MentionedUserIDs: []string{amActive1},
	})
	if err != nil {
		t.Fatalf("CreateMessage: %v", err)
	}
	if message.ID == "" {
		t.Fatal("message was not persisted")
	}
	if message.CreatedConversationEventID == "" {
		t.Fatal("persisted membership event ID was not returned for realtime publication")
	}
	if !slices.Equal(message.AutoAddedMemberIDs, []string{amActive1}) || message.MemberCount != 2 {
		t.Fatalf("auto-add realtime metadata = ids=%v count=%d, want [%s] and 2", message.AutoAddedMemberIDs, message.MemberCount, amActive1)
	}
	if got := countChannelMembers(t, pool, ctx, amPrivate); got != 2 {
		t.Fatalf("successful message memberships = %d, want sender plus mentioned target", got)
	}
	if got := countAutoAddEvents(t, pool, ctx, amPrivate, ""); got != 1 {
		t.Fatalf("successful message events = %d, want one aggregated event", got)
	}

	_, err = store.CreateMessage(ctx, storage.CreateMessageInput{
		WorkspaceID: amWS, ChannelID: amPrivate, SenderID: amActive1,
		BodyText: "forged add", BodyFormat: domain.MessageBodyFormatV3,
		MentionedUserIDs: []string{amActive2},
	})
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("ordinary member auto-add err = %v, want non-enumerating ErrNotFound", err)
	}
	if got := countChannelMembers(t, pool, ctx, amPrivate); got != 2 {
		t.Fatalf("unauthorized mention changed memberships to %d", got)
	}
	if got := countAutoAddEvents(t, pool, ctx, amPrivate, ""); got != 1 {
		t.Fatalf("unauthorized mention changed event count to %d", got)
	}
}

func TestPGXCreateMessageAutoAddsMentionedGroupParticipantAtomicallyPostgreSQL(t *testing.T) {
	pool, ctx := addMembersPostgres(t)
	store := storage.NewPGXMessageStore(pool)

	_, err := store.CreateMessage(ctx, storage.CreateMessageInput{
		WorkspaceID: amWS, DMConversationID: amGroup, SenderID: amActive1,
		BodyText: "mention", BodyFormat: domain.MessageBodyFormatV3,
		ParentMessageID:  "f1000000-0000-4000-8000-000000000099",
		MentionedUserIDs: []string{amActive2},
	})
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("invalid message reference err = %v, want ErrNotFound", err)
	}
	if got := countDMParticipants(t, pool, ctx, amGroup); got != 1 {
		t.Fatalf("failed message left %d participants, want sender only", got)
	}
	if got := countAutoAddEvents(t, pool, ctx, "", amGroup); got != 0 {
		t.Fatalf("failed message left %d membership events, want none", got)
	}

	message, err := store.CreateMessage(ctx, storage.CreateMessageInput{
		WorkspaceID: amWS, DMConversationID: amGroup, SenderID: amActive1,
		BodyText: "mention", BodyFormat: domain.MessageBodyFormatV3,
		MentionedUserIDs: []string{amActive2},
	})
	if err != nil {
		t.Fatalf("CreateMessage: %v", err)
	}
	if message.CreatedConversationEventID == "" {
		t.Fatal("persisted membership event ID was not returned for realtime publication")
	}
	if !slices.Equal(message.AutoAddedMemberIDs, []string{amActive2}) || message.MemberCount != 2 {
		t.Fatalf("auto-add realtime metadata = ids=%v count=%d, want [%s] and 2", message.AutoAddedMemberIDs, message.MemberCount, amActive2)
	}
	if got := countDMParticipants(t, pool, ctx, amGroup); got != 2 {
		t.Fatalf("successful message participants = %d, want sender plus mentioned target", got)
	}
	if got := countAutoAddEvents(t, pool, ctx, "", amGroup); got != 1 {
		t.Fatalf("successful message events = %d, want one aggregated event", got)
	}
}
