package service_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/service"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

// ---- helpers ---------------------------------------------------------------

func publicActiveChannel(workspaceID, channelID string) domain.Channel {
	return domain.Channel{
		ID: channelID, WorkspaceID: workspaceID,
		Type: domain.ChannelTypePublic, Status: domain.ChannelStatusActive,
	}
}

func privateActiveChannel(workspaceID, channelID string) domain.Channel {
	return domain.Channel{
		ID: channelID, WorkspaceID: workspaceID,
		Type: domain.ChannelTypePrivate, Status: domain.ChannelStatusActive,
	}
}

func activeDMConversation(workspaceID, convID string) domain.DMConversation {
	return domain.DMConversation{
		ID: convID, WorkspaceID: workspaceID,
		Type: domain.DMConversationTypeDirect, Status: domain.DMConversationStatusActive,
	}
}

// ---- fakeMessageStore ------------------------------------------------------

type fakeMessageStore struct {
	createdMessage          domain.Message
	createErr               error
	afterCreate             func()
	channelMessages         []domain.Message
	listChannelErr          error
	listChannelNextCursor   *storage.MessageCursor
	dmMessages              []domain.Message
	listDMErr               error
	listDMNextCursor        *storage.MessageCursor
	messagesByKey           map[string]domain.Message // key: workspaceID+":"+messageID
	getByIDErr              error
	validateRefTargetErr    error
	mentionLabels           map[string]string
	resolveMentionErr       error
	authorizedMentionLabels map[string]string
	resolveAuthorizedErr    error

	lastCreateInput        storage.CreateMessageInput
	createCalls            int
	listChannelCalls       int
	listDMCalls            int
	resolveMentionCalls    int
	resolveAuthorizedCalls int
}

func (f *fakeMessageStore) CreateMessage(_ context.Context, input storage.CreateMessageInput) (domain.Message, error) {
	f.createCalls++
	f.lastCreateInput = input
	if f.createErr == nil && f.afterCreate != nil {
		f.afterCreate()
	}
	return f.createdMessage, f.createErr
}

func (f *fakeMessageStore) GetMessageByIDInWorkspace(_ context.Context, workspaceID, messageID, _ string) (domain.Message, error) {
	if f.getByIDErr != nil {
		return domain.Message{}, f.getByIDErr
	}
	if f.messagesByKey != nil {
		key := workspaceID + ":" + messageID
		if msg, ok := f.messagesByKey[key]; ok {
			return msg, nil
		}
		return domain.Message{}, domain.ErrNotFound
	}
	return domain.Message{}, domain.ErrNotFound
}

func (f *fakeMessageStore) ValidateRefMessageInTarget(_ context.Context, _, _, _, _, _ string) error {
	return f.validateRefTargetErr
}

func (f *fakeMessageStore) ResolveMentionLabels(_ context.Context, _ string, _, _ []string) (map[string]string, error) {
	f.resolveMentionCalls++
	return f.mentionLabels, f.resolveMentionErr
}

func (f *fakeMessageStore) ResolveAuthorizedMentionLabels(_ context.Context, _, _, _ string, _, _ []string) (map[string]string, error) {
	f.resolveAuthorizedCalls++
	if f.authorizedMentionLabels == nil {
		return f.mentionLabels, f.resolveAuthorizedErr
	}
	return f.authorizedMentionLabels, f.resolveAuthorizedErr
}

func (f *fakeMessageStore) ListChannelMessages(_ context.Context, _ storage.ListChannelMessagesInput) (storage.ListMessagesResult, error) {
	f.listChannelCalls++
	return storage.ListMessagesResult{Messages: f.channelMessages, NextCursor: f.listChannelNextCursor}, f.listChannelErr
}

func (f *fakeMessageStore) ListDMMessages(_ context.Context, _ storage.ListDMMessagesInput) (storage.ListMessagesResult, error) {
	f.listDMCalls++
	return storage.ListMessagesResult{Messages: f.dmMessages, NextCursor: f.listDMNextCursor}, f.listDMErr
}

// ---- channel message tests -------------------------------------------------

func TestMessageService_CreateChannelMessage_PublicChannelActiveWorkspaceMemberSucceeds(t *testing.T) {
	ch := publicActiveChannel("ws-1", "ch-1")
	channels := &fakeChannelStore{visibleChannel: ch}
	msgs := &fakeMessageStore{createdMessage: domain.Message{
		ID: "msg-1", WorkspaceID: "ws-1", ChannelID: "ch-1",
		SenderID: user1, Kind: domain.MessageKindUser, BodyText: "hello",
		Status: domain.MessageStatusActive,
	}}

	got, err := service.NewMessageService(channels, &fakeDMStore{}, msgs).
		CreateChannelMessage(context.Background(), service.CreateChannelMessageInput{
			WorkspaceID: "ws-1", ChannelID: "ch-1", SenderID: user1, BodyText: "hello",
			BodyFormat: domain.MessageBodyFormatV2,
		})
	if err != nil {
		t.Fatalf("CreateChannelMessage: %v", err)
	}
	if got.ID != "msg-1" || got.Status != domain.MessageStatusActive {
		t.Fatalf("unexpected message: %+v", got)
	}
	if msgs.createCalls != 1 {
		t.Fatalf("expected one CreateMessage call, got %d", msgs.createCalls)
	}
	if channels.getVisibleByIDCalls != 1 {
		t.Fatalf("expected one visibility check, got %d", channels.getVisibleByIDCalls)
	}
	if msgs.lastCreateInput.BodyFormat != domain.MessageBodyFormatV2 {
		t.Fatalf("expected v2 forwarded to storage, got %q", msgs.lastCreateInput.BodyFormat)
	}
}

func TestMessageService_CreateChannelMessage_RejectsUnknownBodyFormat(t *testing.T) {
	svc := service.NewMessageService(&fakeChannelStore{}, &fakeDMStore{}, &fakeMessageStore{})
	_, err := svc.CreateChannelMessage(context.Background(), service.CreateChannelMessageInput{
		WorkspaceID: "ws-1", ChannelID: "ch-1", SenderID: user1, BodyText: "hello",
		BodyFormat: "v4",
	})
	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput, got %v", err)
	}
}

func TestMessageService_CreateChannelMessage_AcceptsV3BodyFormat(t *testing.T) {
	channels := &fakeChannelStore{visibleChannel: publicActiveChannel("ws-1", "ch-1")}
	msgs := &fakeMessageStore{createdMessage: domain.Message{ID: "msg-v3"}}

	_, err := service.NewMessageService(channels, &fakeDMStore{}, msgs).
		CreateChannelMessage(context.Background(), service.CreateChannelMessageInput{
			WorkspaceID: "ws-1", ChannelID: "ch-1", SenderID: user1, BodyText: "hello",
			BodyFormat: "v3",
		})
	if err != nil {
		t.Fatalf("CreateChannelMessage v3: %v", err)
	}
	if msgs.lastCreateInput.BodyFormat != "v3" {
		t.Fatalf("expected v3 forwarded to storage, got %q", msgs.lastCreateInput.BodyFormat)
	}
}

func TestMessageService_CreateChannelMessage_ExtractsAndCanonicalizesMentions(t *testing.T) {
	const userID = "11111111-1111-1111-1111-111111111111"
	const channelID = "22222222-2222-2222-2222-222222222222"
	channels := &fakeChannelStore{visibleChannel: publicActiveChannel("ws-1", "ch-1")}
	msgs := &fakeMessageStore{
		createdMessage: domain.Message{ID: "msg-v3"},
		mentionLabels: map[string]string{
			"user:" + userID:       "Alice",
			"channel:" + channelID: "geral",
		},
	}
	body := `@[Spoofed](mention:user:` + userID + `) @[old](mention:channel:` + channelID + `)`

	_, err := service.NewMessageService(channels, &fakeDMStore{}, msgs).
		CreateChannelMessage(context.Background(), service.CreateChannelMessageInput{
			WorkspaceID: "ws-1", ChannelID: "ch-1", SenderID: user1,
			BodyText: body, BodyFormat: "v3",
		})
	if err != nil {
		t.Fatalf("CreateChannelMessage: %v", err)
	}
	if got := msgs.lastCreateInput.MentionedUserIDs; len(got) != 1 || got[0] != userID {
		t.Fatalf("unexpected mentioned users: %#v", got)
	}
	if got := msgs.lastCreateInput.MentionedChannelIDs; len(got) != 1 || got[0] != channelID {
		t.Fatalf("unexpected mentioned channels: %#v", got)
	}
	wantBody := `@[Alice](mention:user:` + userID + `) @[geral](mention:channel:` + channelID + `)`
	if msgs.lastCreateInput.BodyText != wantBody {
		t.Fatalf("body = %q, want %q", msgs.lastCreateInput.BodyText, wantBody)
	}
}

func TestMessageService_CreateChannelMessage_RejectsUnauthorizedMentionBeforeInsert(t *testing.T) {
	const userID = "99999999-9999-9999-9999-999999999999"
	channels := &fakeChannelStore{visibleChannel: publicActiveChannel("ws-1", "ch-1")}
	msgs := &fakeMessageStore{authorizedMentionLabels: map[string]string{}}

	_, err := service.NewMessageService(channels, &fakeDMStore{}, msgs).
		CreateChannelMessage(context.Background(), service.CreateChannelMessageInput{
			WorkspaceID: "ws-1", ChannelID: "ch-1", SenderID: user1,
			BodyText: `@[Outsider](mention:user:` + userID + `)`, BodyFormat: domain.MessageBodyFormatV3,
		})

	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("expected non-enumerating invalid input, got %v", err)
	}
	if msgs.createCalls != 0 {
		t.Fatalf("unauthorized mention must fail before insert, got %d CreateMessage calls", msgs.createCalls)
	}
}

func TestMessageService_CreateChannelMessage_MentionRemovedAfterPreflightIsRejected(t *testing.T) {
	const userID = "11111111-1111-1111-1111-111111111111"
	channels := &fakeChannelStore{visibleChannel: publicActiveChannel("ws-1", "ch-1")}
	msgs := &fakeMessageStore{
		authorizedMentionLabels: map[string]string{"user:" + userID: "Alice"},
		createErr:               domain.ErrNotFound,
	}

	_, err := service.NewMessageService(channels, &fakeDMStore{}, msgs).
		CreateChannelMessage(context.Background(), service.CreateChannelMessageInput{
			WorkspaceID: "ws-1", ChannelID: "ch-1", SenderID: user1,
			BodyText: `@[Alice](mention:user:` + userID + `)`, BodyFormat: domain.MessageBodyFormatV3,
		})

	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("atomic storage backstop must reject a membership race, got %v", err)
	}
	if msgs.resolveAuthorizedCalls != 1 || msgs.createCalls != 1 {
		t.Fatalf("expected preflight then atomic insert, resolve=%d create=%d", msgs.resolveAuthorizedCalls, msgs.createCalls)
	}
}

func TestMessageService_CreateChannelMessage_DoesNotInterpretMentionSyntaxBeforeV3(t *testing.T) {
	channels := &fakeChannelStore{visibleChannel: publicActiveChannel("ws-1", "ch-1")}
	msgs := &fakeMessageStore{createdMessage: domain.Message{ID: "msg-v2"}}
	body := `@[literal](mention:user:11111111-1111-1111-1111-111111111111)`

	_, err := service.NewMessageService(channels, &fakeDMStore{}, msgs).
		CreateChannelMessage(context.Background(), service.CreateChannelMessageInput{
			WorkspaceID: "ws-1", ChannelID: "ch-1", SenderID: user1,
			BodyText: body, BodyFormat: domain.MessageBodyFormatV2,
		})
	if err != nil {
		t.Fatalf("CreateChannelMessage: %v", err)
	}
	if len(msgs.lastCreateInput.MentionedUserIDs) != 0 {
		t.Fatalf("v2 text must not create mentions: %#v", msgs.lastCreateInput.MentionedUserIDs)
	}
}

func TestMessageService_CreateChannelMessage_DefaultsMissingBodyFormatToV1(t *testing.T) {
	channels := &fakeChannelStore{visibleChannel: publicActiveChannel("ws-1", "ch-1")}
	msgs := &fakeMessageStore{createdMessage: domain.Message{ID: "msg-legacy"}}
	_, err := service.NewMessageService(channels, &fakeDMStore{}, msgs).
		CreateChannelMessage(context.Background(), service.CreateChannelMessageInput{
			WorkspaceID: "ws-1", ChannelID: "ch-1", SenderID: user1, BodyText: "legacy",
		})
	if err != nil {
		t.Fatalf("CreateChannelMessage: %v", err)
	}
	if msgs.lastCreateInput.BodyFormat != domain.MessageBodyFormatV1 {
		t.Fatalf("expected missing format to default to v1, got %q", msgs.lastCreateInput.BodyFormat)
	}
}

func TestMessageService_CreateChannelMessage_PrivateChannelMemberSucceeds(t *testing.T) {
	ch := privateActiveChannel("ws-1", "ch-private")
	channels := &fakeChannelStore{visibleChannel: ch}
	msgs := &fakeMessageStore{createdMessage: domain.Message{
		ID: "msg-2", WorkspaceID: "ws-1", ChannelID: "ch-private", SenderID: user1,
		Kind: domain.MessageKindUser, BodyText: "private msg", Status: domain.MessageStatusActive,
	}}

	got, err := service.NewMessageService(channels, &fakeDMStore{}, msgs).
		CreateChannelMessage(context.Background(), service.CreateChannelMessageInput{
			WorkspaceID: "ws-1", ChannelID: "ch-private", SenderID: user1, BodyText: "private msg",
		})
	if err != nil {
		t.Fatalf("CreateChannelMessage in private channel: %v", err)
	}
	if got.ChannelID != "ch-private" {
		t.Fatalf("unexpected channel id: %+v", got)
	}
}

func TestMessageService_CreateChannelMessage_PrivateChannelNonMemberDeniedNonEnumerating(t *testing.T) {
	channels := &fakeChannelStore{getVisibleErr: domain.ErrNotFound}
	svc := service.NewMessageService(channels, &fakeDMStore{}, &fakeMessageStore{})

	_, err := svc.CreateChannelMessage(context.Background(), service.CreateChannelMessageInput{
		WorkspaceID: "ws-1", ChannelID: "ch-private", SenderID: user2, BodyText: "hi",
	})
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected ErrNotFound for private channel non-member, got %v", err)
	}
}

func TestMessageService_CreateChannelMessage_ArchivedChannelDenied(t *testing.T) {
	// Archived channels are not returned by GetVisibleChannelByID (only active ones are).
	channels := &fakeChannelStore{getVisibleErr: domain.ErrNotFound}
	svc := service.NewMessageService(channels, &fakeDMStore{}, &fakeMessageStore{})

	_, err := svc.CreateChannelMessage(context.Background(), service.CreateChannelMessageInput{
		WorkspaceID: "ws-1", ChannelID: "ch-archived", SenderID: user1, BodyText: "hi",
	})
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected ErrNotFound for archived channel, got %v", err)
	}
}

func TestMessageService_CreateChannelMessage_SuspendedWorkspaceMemberDenied(t *testing.T) {
	// Suspended members fail the workspace_members JOIN in GetVisibleChannelByID.
	channels := &fakeChannelStore{getVisibleErr: domain.ErrNotFound}
	svc := service.NewMessageService(channels, &fakeDMStore{}, &fakeMessageStore{})

	_, err := svc.CreateChannelMessage(context.Background(), service.CreateChannelMessageInput{
		WorkspaceID: "ws-1", ChannelID: "ch-1", SenderID: "suspended-user", BodyText: "hi",
	})
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected ErrNotFound for suspended workspace member, got %v", err)
	}
}

func TestMessageService_CreateChannelMessage_CrossWorkspaceTargetDenied(t *testing.T) {
	// Channel belongs to ws-2 but input claims ws-1: visibility check returns ErrNotFound.
	ch := publicActiveChannel("ws-2", "ch-other")
	channels := &fakeChannelStore{visibleChannel: ch} // workspaceID mismatch → ErrNotFound
	svc := service.NewMessageService(channels, &fakeDMStore{}, &fakeMessageStore{})

	_, err := svc.CreateChannelMessage(context.Background(), service.CreateChannelMessageInput{
		WorkspaceID: "ws-1", ChannelID: "ch-other", SenderID: user1, BodyText: "hi",
	})
	// fakeChannelStore.GetVisibleChannelByID returns ErrNotFound when workspaceID mismatches.
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected ErrNotFound for cross-workspace target, got %v", err)
	}
}

func TestMessageService_CreateChannelMessage_EmptyBodyDenied(t *testing.T) {
	ch := publicActiveChannel("ws-1", "ch-1")
	channels := &fakeChannelStore{visibleChannel: ch}
	svc := service.NewMessageService(channels, &fakeDMStore{}, &fakeMessageStore{})

	_, err := svc.CreateChannelMessage(context.Background(), service.CreateChannelMessageInput{
		WorkspaceID: "ws-1", ChannelID: "ch-1", SenderID: user1, BodyText: "",
	})
	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput for empty body, got %v", err)
	}
}

func TestMessageService_CreateChannelMessage_BodyTooLongDenied(t *testing.T) {
	ch := publicActiveChannel("ws-1", "ch-1")
	channels := &fakeChannelStore{visibleChannel: ch}
	svc := service.NewMessageService(channels, &fakeDMStore{}, &fakeMessageStore{})

	_, err := svc.CreateChannelMessage(context.Background(), service.CreateChannelMessageInput{
		WorkspaceID: "ws-1", ChannelID: "ch-1", SenderID: user1, BodyText: longString(40_001),
	})
	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput for oversized body, got %v", err)
	}
}

func TestMessageService_CreateChannelMessage_ServiceOwnsStatusAndTimestamps(t *testing.T) {
	ch := publicActiveChannel("ws-1", "ch-1")
	channels := &fakeChannelStore{visibleChannel: ch}
	msgs := &fakeMessageStore{createdMessage: domain.Message{
		ID: "msg-1", WorkspaceID: "ws-1", ChannelID: "ch-1",
		SenderID: user1, Kind: domain.MessageKindUser, BodyText: "test",
		Status: domain.MessageStatusActive,
	}}

	_, err := service.NewMessageService(channels, &fakeDMStore{}, msgs).
		CreateChannelMessage(context.Background(), service.CreateChannelMessageInput{
			WorkspaceID: "ws-1", ChannelID: "ch-1", SenderID: user1, BodyText: "test",
		})
	if err != nil {
		t.Fatalf("CreateChannelMessage: %v", err)
	}
	in := msgs.lastCreateInput
	// Service must set workspace, channel, sender from validated input; never allow mass-assign.
	if in.WorkspaceID != "ws-1" {
		t.Fatalf("service must set workspace_id, got %q", in.WorkspaceID)
	}
	if in.ChannelID != "ch-1" {
		t.Fatalf("service must set channel_id, got %q", in.ChannelID)
	}
	if in.SenderID != user1 {
		t.Fatalf("service must set sender_id, got %q", in.SenderID)
	}
	// DMConversationID must be empty for channel messages.
	if in.DMConversationID != "" {
		t.Fatalf("DMConversationID must be empty for channel messages, got %q", in.DMConversationID)
	}
	// Kind is always set by the service.
	if in.Kind != domain.MessageKindUser {
		t.Fatalf("service must default kind to user, got %q", in.Kind)
	}
}

func TestMessageService_CreateChannelMessage_EmptyRequiredFieldsDenied(t *testing.T) {
	svc := service.NewMessageService(&fakeChannelStore{}, &fakeDMStore{}, &fakeMessageStore{})
	cases := []service.CreateChannelMessageInput{
		{ChannelID: "ch-1", SenderID: user1, BodyText: "hi"},
		{WorkspaceID: "ws-1", SenderID: user1, BodyText: "hi"},
		{WorkspaceID: "ws-1", ChannelID: "ch-1", BodyText: "hi"},
	}
	for _, tc := range cases {
		_, err := svc.CreateChannelMessage(context.Background(), tc)
		if !errors.Is(err, domain.ErrInvalidInput) {
			t.Fatalf("expected ErrInvalidInput for input %+v, got %v", tc, err)
		}
	}
}

// ---- DM message tests ------------------------------------------------------

func TestMessageService_CreateDMMessage_ActiveParticipantSucceeds(t *testing.T) {
	conv := activeDMConversation("ws-1", "dm-1")
	dms := &fakeDMStore{visibleConversation: conv}
	msgs := &fakeMessageStore{createdMessage: domain.Message{
		ID: "msg-3", WorkspaceID: "ws-1", DMConversationID: "dm-1",
		SenderID: user1, Kind: domain.MessageKindUser, BodyText: "hey",
		Status: domain.MessageStatusActive,
	}}

	got, err := service.NewMessageService(&fakeChannelStore{}, dms, msgs).
		CreateDMMessage(context.Background(), service.CreateDMMessageInput{
			WorkspaceID: "ws-1", ConversationID: "dm-1", SenderID: user1, BodyText: "hey",
		})
	if err != nil {
		t.Fatalf("CreateDMMessage: %v", err)
	}
	if got.ID != "msg-3" || got.DMConversationID != "dm-1" {
		t.Fatalf("unexpected message: %+v", got)
	}
	if msgs.createCalls != 1 {
		t.Fatalf("expected one CreateMessage call, got %d", msgs.createCalls)
	}
	in := msgs.lastCreateInput
	if in.ChannelID != "" {
		t.Fatalf("ChannelID must be empty for DM messages, got %q", in.ChannelID)
	}
	if in.DMConversationID != "dm-1" {
		t.Fatalf("DMConversationID must be set, got %q", in.DMConversationID)
	}
}

func TestMessageService_CreateDMMessage_NonParticipantDeniedNonEnumerating(t *testing.T) {
	dms := &fakeDMStore{getVisibleErr: domain.ErrNotFound}
	svc := service.NewMessageService(&fakeChannelStore{}, dms, &fakeMessageStore{})

	_, err := svc.CreateDMMessage(context.Background(), service.CreateDMMessageInput{
		WorkspaceID: "ws-1", ConversationID: "dm-1", SenderID: user2, BodyText: "hi",
	})
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected ErrNotFound for non-participant, got %v", err)
	}
}

func TestMessageService_CreateDMMessage_SuspendedWorkspaceMemberDenied(t *testing.T) {
	// Suspended members fail the workspace_members JOIN in GetVisibleConversationByID.
	dms := &fakeDMStore{getVisibleErr: domain.ErrNotFound}
	svc := service.NewMessageService(&fakeChannelStore{}, dms, &fakeMessageStore{})

	_, err := svc.CreateDMMessage(context.Background(), service.CreateDMMessageInput{
		WorkspaceID: "ws-1", ConversationID: "dm-1", SenderID: "suspended-user", BodyText: "hi",
	})
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected ErrNotFound for suspended workspace member, got %v", err)
	}
}

func TestMessageService_CreateDMMessage_CrossWorkspaceTargetDenied(t *testing.T) {
	// GetVisibleConversationByID requires workspace match; different workspace → ErrNotFound.
	dms := &fakeDMStore{getVisibleErr: domain.ErrNotFound}
	svc := service.NewMessageService(&fakeChannelStore{}, dms, &fakeMessageStore{})

	_, err := svc.CreateDMMessage(context.Background(), service.CreateDMMessageInput{
		WorkspaceID: "ws-1", ConversationID: "dm-other-ws", SenderID: user1, BodyText: "hi",
	})
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected ErrNotFound for cross-workspace DM, got %v", err)
	}
}

func TestMessageService_CreateDMMessage_EmptyRequiredFieldsDenied(t *testing.T) {
	svc := service.NewMessageService(&fakeChannelStore{}, &fakeDMStore{}, &fakeMessageStore{})
	cases := []service.CreateDMMessageInput{
		{ConversationID: "dm-1", SenderID: user1, BodyText: "hi"},
		{WorkspaceID: "ws-1", SenderID: user1, BodyText: "hi"},
		{WorkspaceID: "ws-1", ConversationID: "dm-1", BodyText: "hi"},
	}
	for _, tc := range cases {
		_, err := svc.CreateDMMessage(context.Background(), tc)
		if !errors.Is(err, domain.ErrInvalidInput) {
			t.Fatalf("expected ErrInvalidInput for input %+v, got %v", tc, err)
		}
	}
}

// ---- reference message validation tests ------------------------------------

func TestMessageService_CreateChannelMessage_ValidParentMessageSucceeds(t *testing.T) {
	ch := publicActiveChannel("ws-1", "ch-1")
	channels := &fakeChannelStore{visibleChannel: ch}
	msgs := &fakeMessageStore{
		createdMessage: domain.Message{ID: "msg-child", WorkspaceID: "ws-1", ChannelID: "ch-1",
			SenderID: user1, Status: domain.MessageStatusActive},
		// validateRefTargetErr left nil = valid reference
	}

	_, err := service.NewMessageService(channels, &fakeDMStore{}, msgs).
		CreateChannelMessage(context.Background(), service.CreateChannelMessageInput{
			WorkspaceID:     "ws-1",
			ChannelID:       "ch-1",
			SenderID:        user1,
			BodyText:        "reply",
			ParentMessageID: user1, // reusing user1 UUID as a valid UUID for the test
		})
	if err != nil {
		t.Fatalf("CreateChannelMessage with valid parent: %v", err)
	}
	if msgs.lastCreateInput.ParentMessageID != user1 {
		t.Fatalf("expected parent_message_id to be passed to storage, got %q", msgs.lastCreateInput.ParentMessageID)
	}
}

func TestMessageService_CreateChannelMessage_RefMessageCrossWorkspaceDenied(t *testing.T) {
	ch := publicActiveChannel("ws-1", "ch-1")
	channels := &fakeChannelStore{visibleChannel: ch}
	msgs := &fakeMessageStore{validateRefTargetErr: domain.ErrInvalidMessageReference}

	_, err := service.NewMessageService(channels, &fakeDMStore{}, msgs).
		CreateChannelMessage(context.Background(), service.CreateChannelMessageInput{
			WorkspaceID:     "ws-1",
			ChannelID:       "ch-1",
			SenderID:        user1,
			BodyText:        "reply",
			ParentMessageID: user2,
		})
	if !errors.Is(err, domain.ErrInvalidMessageReference) {
		t.Fatalf("expected ErrInvalidMessageReference for cross-workspace ref, got %v", err)
	}
}

func TestMessageService_CreateChannelMessage_RefMessageCrossTargetDenied(t *testing.T) {
	ch := publicActiveChannel("ws-1", "ch-1")
	channels := &fakeChannelStore{visibleChannel: ch}
	msgs := &fakeMessageStore{validateRefTargetErr: domain.ErrInvalidMessageReference}

	_, err := service.NewMessageService(channels, &fakeDMStore{}, msgs).
		CreateChannelMessage(context.Background(), service.CreateChannelMessageInput{
			WorkspaceID:     "ws-1",
			ChannelID:       "ch-1",
			SenderID:        user1,
			BodyText:        "reply",
			ParentMessageID: user2,
		})
	if !errors.Is(err, domain.ErrInvalidMessageReference) {
		t.Fatalf("expected ErrInvalidMessageReference for cross-target ref, got %v", err)
	}
}

func TestMessageService_CreateChannelMessage_RefMessageChannelToDMDenied(t *testing.T) {
	ch := publicActiveChannel("ws-1", "ch-1")
	channels := &fakeChannelStore{visibleChannel: ch}
	msgs := &fakeMessageStore{validateRefTargetErr: domain.ErrInvalidMessageReference}

	_, err := service.NewMessageService(channels, &fakeDMStore{}, msgs).
		CreateChannelMessage(context.Background(), service.CreateChannelMessageInput{
			WorkspaceID:     "ws-1",
			ChannelID:       "ch-1",
			SenderID:        user1,
			BodyText:        "reply",
			ParentMessageID: user2, // storage returns invalid-ref (channel→DM)
		})
	if !errors.Is(err, domain.ErrInvalidMessageReference) {
		t.Fatalf("expected ErrInvalidMessageReference for channel-to-DM ref, got %v", err)
	}
}

func TestMessageService_CreateChannelMessage_DeletedParentMessageDenied(t *testing.T) {
	ch := publicActiveChannel("ws-1", "ch-1")
	channels := &fakeChannelStore{visibleChannel: ch}
	// Decision: replies to deleted messages are rejected, matching AddFavorite's
	// active-message rule and keeping invalid-reference reasons non-enumerating.
	msgs := &fakeMessageStore{validateRefTargetErr: domain.ErrInvalidMessageReference}

	_, err := service.NewMessageService(channels, &fakeDMStore{}, msgs).
		CreateChannelMessage(context.Background(), service.CreateChannelMessageInput{
			WorkspaceID:     "ws-1",
			ChannelID:       "ch-1",
			SenderID:        user1,
			BodyText:        "reply",
			ParentMessageID: user2,
		})
	if !errors.Is(err, domain.ErrInvalidMessageReference) {
		t.Fatalf("expected ErrInvalidMessageReference for deleted parent, got %v", err)
	}
}

func TestMessageService_CreateChannelMessage_ForwardedFromInvalidDenied(t *testing.T) {
	ch := publicActiveChannel("ws-1", "ch-1")
	channels := &fakeChannelStore{visibleChannel: ch}
	msgs := &fakeMessageStore{validateRefTargetErr: domain.ErrInvalidMessageReference}

	_, err := service.NewMessageService(channels, &fakeDMStore{}, msgs).
		CreateChannelMessage(context.Background(), service.CreateChannelMessageInput{
			WorkspaceID:            "ws-1",
			ChannelID:              "ch-1",
			SenderID:               user1,
			BodyText:               "fwd",
			ForwardedFromMessageID: user2,
		})
	if !errors.Is(err, domain.ErrInvalidMessageReference) {
		t.Fatalf("expected ErrInvalidMessageReference for invalid forwarded_from, got %v", err)
	}
}

func TestMessageService_CreateChannelMessage_ReferencedInvalidDenied(t *testing.T) {
	ch := publicActiveChannel("ws-1", "ch-1")
	channels := &fakeChannelStore{visibleChannel: ch}
	msgs := &fakeMessageStore{validateRefTargetErr: domain.ErrInvalidMessageReference}

	_, err := service.NewMessageService(channels, &fakeDMStore{}, msgs).
		CreateChannelMessage(context.Background(), service.CreateChannelMessageInput{
			WorkspaceID:         "ws-1",
			ChannelID:           "ch-1",
			SenderID:            user1,
			BodyText:            "ref",
			ReferencedMessageID: user2,
		})
	if !errors.Is(err, domain.ErrInvalidMessageReference) {
		t.Fatalf("expected ErrInvalidMessageReference for invalid referenced_message, got %v", err)
	}
}

func TestMessageService_CreateChannelMessage_RefMessageInvalidUUIDDenied(t *testing.T) {
	ch := publicActiveChannel("ws-1", "ch-1")
	channels := &fakeChannelStore{visibleChannel: ch}
	svc := service.NewMessageService(channels, &fakeDMStore{}, &fakeMessageStore{})

	_, err := svc.CreateChannelMessage(context.Background(), service.CreateChannelMessageInput{
		WorkspaceID:     "ws-1",
		ChannelID:       "ch-1",
		SenderID:        user1,
		BodyText:        "hi",
		ParentMessageID: "not-a-uuid",
	})
	if !errors.Is(err, domain.ErrInvalidMessageReference) {
		t.Fatalf("expected ErrInvalidMessageReference for invalid UUID ref, got %v", err)
	}
}

func TestMessageService_CreateDMMessage_RefMessageDMToChannelDenied(t *testing.T) {
	dm := domain.DMConversation{
		ID: "dm-1", WorkspaceID: "ws-1", Status: domain.DMConversationStatusActive,
	}
	dms := &fakeDMStore{visibleConversation: dm}
	msgs := &fakeMessageStore{validateRefTargetErr: domain.ErrInvalidMessageReference}

	_, err := service.NewMessageService(&fakeChannelStore{}, dms, msgs).
		CreateDMMessage(context.Background(), service.CreateDMMessageInput{
			WorkspaceID:     "ws-1",
			ConversationID:  "dm-1",
			SenderID:        user1,
			BodyText:        "ref",
			ParentMessageID: user2, // storage returns invalid-ref (DM→channel)
		})
	if !errors.Is(err, domain.ErrInvalidMessageReference) {
		t.Fatalf("expected ErrInvalidMessageReference for DM-to-channel ref, got %v", err)
	}
}

// ---- list messages tests ---------------------------------------------------

func TestMessageService_ListChannelMessages_VisibleChannelDelegatesToStorage(t *testing.T) {
	channels := &fakeChannelStore{visibleChannel: publicActiveChannel("ws-1", "ch-1")}
	msgs := &fakeMessageStore{
		channelMessages: []domain.Message{
			{ID: "m1", WorkspaceID: "ws-1", ChannelID: "ch-1", SenderID: user1,
				BodyText: "first", Status: domain.MessageStatusActive},
			{ID: "m2", WorkspaceID: "ws-1", ChannelID: "ch-1", SenderID: user2,
				BodyText: "second", Status: domain.MessageStatusActive},
		},
	}
	svc := service.NewMessageService(channels, &fakeDMStore{}, msgs)

	got, err := svc.ListChannelMessages(context.Background(), service.ListChannelMessagesInput{
		WorkspaceID: "ws-1", ChannelID: "ch-1", CallerID: user1,
	})
	if err != nil {
		t.Fatalf("ListChannelMessages: %v", err)
	}
	if len(got.Messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(got.Messages))
	}
	if msgs.listChannelCalls != 1 {
		t.Fatalf("expected one ListChannelMessages storage call, got %d", msgs.listChannelCalls)
	}
	if channels.getVisibleByIDCalls != 1 {
		t.Fatalf("expected one channel visibility check, got %d", channels.getVisibleByIDCalls)
	}
}

func TestMessageService_ListChannelMessages_RefreshesMentionLabels(t *testing.T) {
	const userID = "11111111-1111-1111-1111-111111111111"
	channels := &fakeChannelStore{visibleChannel: publicActiveChannel("ws-1", "ch-1")}
	msgs := &fakeMessageStore{
		channelMessages: []domain.Message{{
			ID: "m1", WorkspaceID: "ws-1", ChannelID: "ch-1",
			BodyText: `@[Old](mention:user:` + userID + `)`, BodyFormat: domain.MessageBodyFormatV3,
		}},
		mentionLabels: map[string]string{"user:" + userID: "New Name"},
	}

	got, err := service.NewMessageService(channels, &fakeDMStore{}, msgs).
		ListChannelMessages(context.Background(), service.ListChannelMessagesInput{
			WorkspaceID: "ws-1", ChannelID: "ch-1", CallerID: user1,
		})
	if err != nil {
		t.Fatalf("ListChannelMessages: %v", err)
	}
	want := `@[New Name](mention:user:` + userID + `)`
	if got.Messages[0].BodyText != want {
		t.Fatalf("body = %q, want %q", got.Messages[0].BodyText, want)
	}
}

type cachedMentionLabel struct {
	label     string
	expiresAt time.Time
}

type fakeMentionLabelCache struct {
	now      time.Time
	entries  map[string]cachedMentionLabel
	lastTTL  time.Duration
	setCalls int
	getErr   error
	setErr   error
}

func (f *fakeMentionLabelCache) Get(_ context.Context, workspaceID string, refs []string) (map[string]string, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	labels := make(map[string]string, len(refs))
	for _, ref := range refs {
		entry, ok := f.entries[workspaceID+":"+ref]
		if ok && f.now.Before(entry.expiresAt) {
			labels[ref] = entry.label
		}
	}
	return labels, nil
}

func (f *fakeMentionLabelCache) Set(_ context.Context, workspaceID string, labels map[string]string, ttl time.Duration) error {
	f.setCalls++
	f.lastTTL = ttl
	if f.setErr != nil {
		return f.setErr
	}
	for ref, label := range labels {
		f.entries[workspaceID+":"+ref] = cachedMentionLabel{label: label, expiresAt: f.now.Add(ttl)}
	}
	return nil
}

func TestMessageService_ListChannelMessages_MentionLabelCacheFailureFallsBack(t *testing.T) {
	const userID = "11111111-1111-1111-1111-111111111111"
	channels := &fakeChannelStore{visibleChannel: publicActiveChannel("ws-1", "ch-1")}
	msgs := &fakeMessageStore{
		channelMessages: []domain.Message{{
			ID: "m1", WorkspaceID: "ws-1", ChannelID: "ch-1",
			BodyText: `@[Old](mention:user:` + userID + `)`, BodyFormat: domain.MessageBodyFormatV3,
		}},
		mentionLabels: map[string]string{"user:" + userID: "Database Name"},
	}
	cache := &fakeMentionLabelCache{
		entries: map[string]cachedMentionLabel{}, getErr: errors.New("valkey unavailable"), setErr: errors.New("valkey unavailable"),
	}
	svc := service.NewMessageService(channels, &fakeDMStore{}, msgs)
	svc.SetMentionLabelCache(cache)

	got, err := svc.ListChannelMessages(context.Background(), service.ListChannelMessagesInput{
		WorkspaceID: "ws-1", ChannelID: "ch-1", CallerID: user1,
	})
	if err != nil {
		t.Fatalf("cache failure must not fail message reads: %v", err)
	}
	if msgs.resolveMentionCalls != 1 || cache.setCalls != 1 {
		t.Fatalf("expected database fallback and best-effort cache set, resolves=%d sets=%d", msgs.resolveMentionCalls, cache.setCalls)
	}
	want := `@[Database Name](mention:user:` + userID + `)`
	if got.Messages[0].BodyText != want {
		t.Fatalf("body = %q, want %q", got.Messages[0].BodyText, want)
	}
}

func TestMessageService_ListChannelMessages_MentionLabelCacheHitAndExpiry(t *testing.T) {
	const userID = "11111111-1111-1111-1111-111111111111"
	channels := &fakeChannelStore{visibleChannel: publicActiveChannel("ws-1", "ch-1")}
	msgs := &fakeMessageStore{
		channelMessages: []domain.Message{{
			ID: "m1", WorkspaceID: "ws-1", ChannelID: "ch-1",
			BodyText: `@[Old](mention:user:` + userID + `)`, BodyFormat: domain.MessageBodyFormatV3,
		}},
		mentionLabels: map[string]string{"user:" + userID: "Cached Name"},
	}
	cache := &fakeMentionLabelCache{now: time.Unix(1_700_000_000, 0), entries: map[string]cachedMentionLabel{}}
	svc := service.NewMessageService(channels, &fakeDMStore{}, msgs)
	svc.SetMentionLabelCache(cache)
	input := service.ListChannelMessagesInput{WorkspaceID: "ws-1", ChannelID: "ch-1", CallerID: user1}

	if _, err := svc.ListChannelMessages(context.Background(), input); err != nil {
		t.Fatalf("first ListChannelMessages: %v", err)
	}
	if msgs.resolveMentionCalls != 1 || cache.setCalls != 1 || cache.lastTTL != 45*time.Second {
		t.Fatalf("cache miss must query and cache for 45s: resolves=%d sets=%d ttl=%s", msgs.resolveMentionCalls, cache.setCalls, cache.lastTTL)
	}

	if _, err := svc.ListChannelMessages(context.Background(), input); err != nil {
		t.Fatalf("cached ListChannelMessages: %v", err)
	}
	if msgs.resolveMentionCalls != 1 {
		t.Fatalf("cache hit must avoid a second batched query, got %d", msgs.resolveMentionCalls)
	}

	cache.now = cache.now.Add(46 * time.Second)
	msgs.mentionLabels = map[string]string{"user:" + userID: "Fresh Name"}
	got, err := svc.ListChannelMessages(context.Background(), input)
	if err != nil {
		t.Fatalf("expired ListChannelMessages: %v", err)
	}
	if msgs.resolveMentionCalls != 2 {
		t.Fatalf("expired cache must query again, got %d", msgs.resolveMentionCalls)
	}
	want := `@[Fresh Name](mention:user:` + userID + `)`
	if got.Messages[0].BodyText != want {
		t.Fatalf("body = %q, want %q", got.Messages[0].BodyText, want)
	}
}

func TestMessageService_ListChannelMessages_InaccessibleChannelDeniedNonEnumerating(t *testing.T) {
	channels := &fakeChannelStore{getVisibleErr: domain.ErrNotFound}
	msgs := &fakeMessageStore{}
	svc := service.NewMessageService(channels, &fakeDMStore{}, msgs)

	_, err := svc.ListChannelMessages(context.Background(), service.ListChannelMessagesInput{
		WorkspaceID: "ws-1", ChannelID: "ch-private", CallerID: "non-member",
	})
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected ErrNotFound for inaccessible channel, got %v", err)
	}
	if msgs.listChannelCalls != 0 {
		t.Fatalf("inaccessible channel must not list messages, got %d calls", msgs.listChannelCalls)
	}
	if channels.getVisibleByIDCalls != 1 {
		t.Fatalf("expected one channel visibility check, got %d", channels.getVisibleByIDCalls)
	}
}

func TestMessageService_ListDMMessages_VisibleConversationDelegatesToStorage(t *testing.T) {
	dms := &fakeDMStore{visibleConversation: activeDMConversation("ws-1", "dm-1")}
	msgs := &fakeMessageStore{
		dmMessages: []domain.Message{
			{ID: "m3", WorkspaceID: "ws-1", DMConversationID: "dm-1", SenderID: user1,
				BodyText: "dm msg", Status: domain.MessageStatusActive},
		},
	}
	svc := service.NewMessageService(&fakeChannelStore{}, dms, msgs)

	got, err := svc.ListDMMessages(context.Background(), service.ListDMMessagesInput{
		WorkspaceID: "ws-1", ConversationID: "dm-1", CallerID: user1,
	})
	if err != nil {
		t.Fatalf("ListDMMessages: %v", err)
	}
	if len(got.Messages) != 1 {
		t.Fatalf("expected 1 dm message, got %d", len(got.Messages))
	}
	if msgs.listDMCalls != 1 {
		t.Fatalf("expected one ListDMMessages storage call, got %d", msgs.listDMCalls)
	}
	if dms.getVisibleCalls != 1 {
		t.Fatalf("expected one DM visibility check, got %d", dms.getVisibleCalls)
	}
}

func TestMessageService_ListDMMessages_InaccessibleConversationDeniedNonEnumerating(t *testing.T) {
	dms := &fakeDMStore{getVisibleErr: domain.ErrNotFound}
	msgs := &fakeMessageStore{}
	svc := service.NewMessageService(&fakeChannelStore{}, dms, msgs)

	_, err := svc.ListDMMessages(context.Background(), service.ListDMMessagesInput{
		WorkspaceID: "ws-1", ConversationID: "dm-other", CallerID: "non-participant",
	})
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected ErrNotFound for inaccessible DM, got %v", err)
	}
	if msgs.listDMCalls != 0 {
		t.Fatalf("inaccessible DM must not list messages, got %d calls", msgs.listDMCalls)
	}
	if dms.getVisibleCalls != 1 {
		t.Fatalf("expected one DM visibility check, got %d", dms.getVisibleCalls)
	}
}

func TestMessageService_ListChannelMessages_EmptyRequiredFieldsDenied(t *testing.T) {
	svc := service.NewMessageService(&fakeChannelStore{}, &fakeDMStore{}, &fakeMessageStore{})
	cases := []service.ListChannelMessagesInput{
		{ChannelID: "ch-1", CallerID: user1},
		{WorkspaceID: "ws-1", CallerID: user1},
		{WorkspaceID: "ws-1", ChannelID: "ch-1"},
	}
	for _, tc := range cases {
		_, err := svc.ListChannelMessages(context.Background(), tc)
		if !errors.Is(err, domain.ErrInvalidInput) {
			t.Fatalf("expected ErrInvalidInput for input %+v, got %v", tc, err)
		}
	}
}

func TestMessageService_ListDMMessages_EmptyRequiredFieldsDenied(t *testing.T) {
	svc := service.NewMessageService(&fakeChannelStore{}, &fakeDMStore{}, &fakeMessageStore{})
	cases := []service.ListDMMessagesInput{
		{ConversationID: "dm-1", CallerID: user1},
		{WorkspaceID: "ws-1", CallerID: user1},
		{WorkspaceID: "ws-1", ConversationID: "dm-1"},
	}
	for _, tc := range cases {
		_, err := svc.ListDMMessages(context.Background(), tc)
		if !errors.Is(err, domain.ErrInvalidInput) {
			t.Fatalf("expected ErrInvalidInput for input %+v, got %v", tc, err)
		}
	}
}

// ---- ListChannelMessages: cursor and next-cursor paths ---------------------

func TestMessageService_ListChannelMessages_InvalidCursorReturnsErrInvalidCursor(t *testing.T) {
	msgs := &fakeMessageStore{channelMessages: nil}
	svc := service.NewMessageService(&fakeChannelStore{}, &fakeDMStore{}, msgs)

	_, err := svc.ListChannelMessages(context.Background(), service.ListChannelMessagesInput{
		WorkspaceID:  "ws-1",
		ChannelID:    "ch-1",
		CallerID:     user1,
		BeforeCursor: "!!!not-a-valid-cursor!!!",
	})
	if !errors.Is(err, domain.ErrInvalidCursor) {
		t.Fatalf("expected ErrInvalidCursor for invalid cursor, got %v", err)
	}
}

func TestMessageService_ListChannelMessages_ValidCursorWithNextCursorEncoded(t *testing.T) {
	now := time.Now()
	// Storage returns a NextCursor; service must encode it.
	nextCursor := &storage.MessageCursor{CreatedAt: now, ID: "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"}
	msgs := &fakeMessageStore{
		channelMessages:       []domain.Message{{ID: "msg-1", WorkspaceID: "ws-1", ChannelID: "ch-1", SenderID: user1}},
		listChannelNextCursor: nextCursor,
	}
	channels := &fakeChannelStore{visibleChannel: publicActiveChannel("ws-1", "ch-1")}
	svc := service.NewMessageService(channels, &fakeDMStore{}, msgs)

	encoded := storage.EncodeCursor(*nextCursor)
	// Pass the encoded cursor as BeforeCursor to exercise the decode path.
	out, err := svc.ListChannelMessages(context.Background(), service.ListChannelMessagesInput{
		WorkspaceID:  "ws-1",
		ChannelID:    "ch-1",
		CallerID:     user1,
		BeforeCursor: encoded,
	})
	if err != nil {
		t.Fatalf("ListChannelMessages with valid cursor: %v", err)
	}
	if out.NextCursor == "" {
		t.Fatal("expected NextCursor to be set in output when storage returns a cursor")
	}
}

// ---- ListDMMessages: cursor and next-cursor paths --------------------------

func TestMessageService_ListDMMessages_InvalidCursorReturnsErrInvalidCursor(t *testing.T) {
	msgs := &fakeMessageStore{}
	svc := service.NewMessageService(&fakeChannelStore{}, &fakeDMStore{}, msgs)

	_, err := svc.ListDMMessages(context.Background(), service.ListDMMessagesInput{
		WorkspaceID:    "ws-1",
		ConversationID: "dm-1",
		CallerID:       user1,
		BeforeCursor:   "!!!invalid!!!",
	})
	if !errors.Is(err, domain.ErrInvalidCursor) {
		t.Fatalf("expected ErrInvalidCursor for invalid DM cursor, got %v", err)
	}
}

func TestMessageService_ListDMMessages_ValidCursorWithNextCursorEncoded(t *testing.T) {
	now := time.Now()
	nextCursor := &storage.MessageCursor{CreatedAt: now, ID: "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"}
	msgs := &fakeMessageStore{
		dmMessages:       []domain.Message{{ID: "dm-1", WorkspaceID: "ws-1", DMConversationID: "dm-1", SenderID: user1}},
		listDMNextCursor: nextCursor,
	}
	dms := &fakeDMStore{visibleConversation: activeDMConversation("ws-1", "dm-1")}
	svc := service.NewMessageService(&fakeChannelStore{}, dms, msgs)

	encoded := storage.EncodeCursor(*nextCursor)
	out, err := svc.ListDMMessages(context.Background(), service.ListDMMessagesInput{
		WorkspaceID:    "ws-1",
		ConversationID: "dm-1",
		CallerID:       user1,
		BeforeCursor:   encoded,
	})
	if err != nil {
		t.Fatalf("ListDMMessages with valid cursor: %v", err)
	}
	if out.NextCursor == "" {
		t.Fatal("expected NextCursor to be set in DM output when storage returns a cursor")
	}
}
