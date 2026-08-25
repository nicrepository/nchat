package service_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/service"
)

// RF-32. Everything below is input normalisation: whether an attachment may
// actually be linked is a database question, answered atomically with the
// INSERT, and is covered by the PostgreSQL suite. What this file pins down is
// that a malformed or abusive list never reaches a query at all, and that an
// attachment is accepted as a message's whole content.

const attachmentA = "11111111-2222-4333-8444-555555555551"

func attachmentIDs(count int) []string {
	ids := make([]string, count)
	for i := range ids {
		ids[i] = fmt.Sprintf("11111111-2222-4333-8444-%012d", i+1)
	}
	return ids
}

func TestMessageService_CreateChannelMessage_AttachmentOnlyIsValid(t *testing.T) {
	channels := &fakeChannelStore{visibleChannel: publicActiveChannel("ws-1", "ch-1")}
	msgs := &fakeMessageStore{createdMessage: domain.Message{
		ID: "msg-1", WorkspaceID: "ws-1", ChannelID: "ch-1", SenderID: user1,
		Status: domain.MessageStatusActive,
		Attachments: []domain.MessageAttachment{{
			ID: attachmentA, Filename: "relatorio.pdf", ContentType: "application/pdf",
			SizeBytes: 42, Status: "pending_scan", PreviewStatus: "pending",
		}},
	}}

	got, err := service.NewMessageService(channels, &fakeDMStore{}, msgs).
		CreateChannelMessage(context.Background(), service.CreateChannelMessageInput{
			WorkspaceID: "ws-1", ChannelID: "ch-1", SenderID: user1,
			BodyText: "", BodyFormat: domain.MessageBodyFormatV3,
			AttachmentIDs: []string{attachmentA},
		})
	if err != nil {
		t.Fatalf("attachment-only message must be accepted: %v", err)
	}
	if len(got.Attachments) != 1 || got.Attachments[0].ID != attachmentA {
		t.Fatalf("unexpected attachments: %+v", got.Attachments)
	}
	if len(msgs.lastCreateInput.AttachmentIDs) != 1 ||
		msgs.lastCreateInput.AttachmentIDs[0] != attachmentA {
		t.Fatalf("attachment ids must reach storage: %+v", msgs.lastCreateInput.AttachmentIDs)
	}
}

func TestMessageService_CreateChannelMessage_TextPlusAttachmentIsValid(t *testing.T) {
	channels := &fakeChannelStore{visibleChannel: publicActiveChannel("ws-1", "ch-1")}
	msgs := &fakeMessageStore{createdMessage: domain.Message{ID: "msg-1", BodyText: "veja"}}

	if _, err := service.NewMessageService(channels, &fakeDMStore{}, msgs).
		CreateChannelMessage(context.Background(), service.CreateChannelMessageInput{
			WorkspaceID: "ws-1", ChannelID: "ch-1", SenderID: user1,
			BodyText: "veja", BodyFormat: domain.MessageBodyFormatV3,
			AttachmentIDs: []string{attachmentA},
		}); err != nil {
		t.Fatalf("text plus attachment must be accepted: %v", err)
	}
	if msgs.lastCreateInput.BodyText != "veja" || len(msgs.lastCreateInput.AttachmentIDs) != 1 {
		t.Fatalf("both must reach storage: %+v", msgs.lastCreateInput)
	}
}

func TestMessageService_CreateDMMessage_AttachmentOnlyIsValid(t *testing.T) {
	dms := &fakeDMStore{visibleConversation: domain.DMConversation{
		ID: "dm-1", WorkspaceID: "ws-1", Status: domain.DMConversationStatusActive,
	}}
	msgs := &fakeMessageStore{createdMessage: domain.Message{ID: "msg-1"}}

	if _, err := service.NewMessageService(&fakeChannelStore{}, dms, msgs).
		CreateDMMessage(context.Background(), service.CreateDMMessageInput{
			WorkspaceID: "ws-1", ConversationID: "dm-1", SenderID: user1,
			BodyText: "", BodyFormat: domain.MessageBodyFormatV2,
			AttachmentIDs: []string{attachmentA},
		}); err != nil {
		t.Fatalf("attachment-only DM must be accepted: %v", err)
	}
}

// The empty send that has always been refused stays refused.
func TestMessageService_CreateMessage_EmptyBodyWithoutAttachmentIsRejected(t *testing.T) {
	channels := &fakeChannelStore{visibleChannel: publicActiveChannel("ws-1", "ch-1")}
	msgs := &fakeMessageStore{}
	svc := service.NewMessageService(channels, &fakeDMStore{}, msgs)

	for _, attachmentIDs := range [][]string{nil, {}} {
		_, err := svc.CreateChannelMessage(context.Background(), service.CreateChannelMessageInput{
			WorkspaceID: "ws-1", ChannelID: "ch-1", SenderID: user1,
			BodyText: "   ", BodyFormat: domain.MessageBodyFormatV3, AttachmentIDs: attachmentIDs,
		})
		if !errors.Is(err, domain.ErrInvalidInput) {
			t.Fatalf("expected ErrInvalidInput, got %v", err)
		}
	}
	if msgs.createCalls != 0 {
		t.Fatalf("nothing may be persisted, got %d create calls", msgs.createCalls)
	}
}

func TestMessageService_CreateMessage_RejectsMalformedAttachmentLists(t *testing.T) {
	channels := &fakeChannelStore{visibleChannel: publicActiveChannel("ws-1", "ch-1")}
	msgs := &fakeMessageStore{}
	svc := service.NewMessageService(channels, &fakeDMStore{}, msgs)

	for name, attachmentIDs := range map[string][]string{
		"not a uuid":    {"not-a-uuid"},
		"empty id":      {""},
		"blank id":      {"   "},
		"nil uuid":      {"00000000-0000-0000-0000-000000000000"},
		"duplicate":     {attachmentA, attachmentA},
		"trailing junk": {attachmentA + "x"},
		"more than ten": attachmentIDs(11),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := svc.CreateChannelMessage(context.Background(), service.CreateChannelMessageInput{
				WorkspaceID: "ws-1", ChannelID: "ch-1", SenderID: user1,
				BodyText: "texto", BodyFormat: domain.MessageBodyFormatV3,
				AttachmentIDs: attachmentIDs,
			})
			if !errors.Is(err, domain.ErrInvalidInput) {
				t.Fatalf("expected ErrInvalidInput, got %v", err)
			}
		})
	}
	if msgs.createCalls != 0 {
		t.Fatalf("no malformed list may reach storage, got %d create calls", msgs.createCalls)
	}
}

func TestMessageService_CreateChannelMessage_PreservesTenAttachmentIDsInOrder(t *testing.T) {
	channels := &fakeChannelStore{visibleChannel: publicActiveChannel("ws-1", "ch-1")}
	msgs := &fakeMessageStore{createdMessage: domain.Message{ID: "msg-1"}}
	want := attachmentIDs(10)

	_, err := service.NewMessageService(channels, &fakeDMStore{}, msgs).
		CreateChannelMessage(context.Background(), service.CreateChannelMessageInput{
			WorkspaceID: "ws-1", ChannelID: "ch-1", SenderID: user1,
			BodyText: "lote", BodyFormat: domain.MessageBodyFormatV3, AttachmentIDs: want,
		})
	if err != nil {
		t.Fatalf("ten attachments must be accepted: %v", err)
	}
	if !slices.Equal(msgs.lastCreateInput.AttachmentIDs, want) {
		t.Fatalf("order changed: got %v want %v", msgs.lastCreateInput.AttachmentIDs, want)
	}
}

func TestMessageService_MessageAttachmentLimitsReachValidationAndAtomicStore(t *testing.T) {
	channels := &fakeChannelStore{visibleChannel: publicActiveChannel("ws-1", "ch-1")}
	msgs := &fakeMessageStore{createdMessage: domain.Message{ID: "msg-1"}}
	svc := service.NewMessageService(channels, &fakeDMStore{}, msgs).
		WithMessageAttachmentLimits(2, 12345)

	_, err := svc.CreateChannelMessage(context.Background(), service.CreateChannelMessageInput{
		WorkspaceID: "ws-1", ChannelID: "ch-1", SenderID: user1,
		BodyText: "lote", BodyFormat: domain.MessageBodyFormatV3,
		AttachmentIDs: attachmentIDs(3),
	})
	if !errors.Is(err, domain.ErrInvalidInput) || msgs.createCalls != 0 {
		t.Fatalf("configured count was not enforced before storage: err=%v calls=%d", err, msgs.createCalls)
	}

	_, err = svc.CreateChannelMessage(context.Background(), service.CreateChannelMessageInput{
		WorkspaceID: "ws-1", ChannelID: "ch-1", SenderID: user1,
		BodyText: "lote", BodyFormat: domain.MessageBodyFormatV3,
		AttachmentIDs: attachmentIDs(2),
	})
	if err != nil {
		t.Fatalf("configured count should accept two: %v", err)
	}
	if msgs.lastCreateInput.MaxAttachmentBytes != 12345 {
		t.Fatalf("aggregate limit did not reach atomic store: %d", msgs.lastCreateInput.MaxAttachmentBytes)
	}
}

// An id is canonicalised before it is used, so a client cannot smuggle a
// differently-formatted spelling of the same UUID past a comparison.
func TestMessageService_CreateChannelMessage_CanonicalisesAttachmentIDs(t *testing.T) {
	channels := &fakeChannelStore{visibleChannel: publicActiveChannel("ws-1", "ch-1")}
	msgs := &fakeMessageStore{createdMessage: domain.Message{ID: "msg-1"}}

	if _, err := service.NewMessageService(channels, &fakeDMStore{}, msgs).
		CreateChannelMessage(context.Background(), service.CreateChannelMessageInput{
			WorkspaceID: "ws-1", ChannelID: "ch-1", SenderID: user1,
			BodyText: "texto", BodyFormat: domain.MessageBodyFormatV3,
			AttachmentIDs: []string{"  11111111-2222-4333-8444-555555555551  "},
		}); err != nil {
		t.Fatalf("CreateChannelMessage: %v", err)
	}
	if got := msgs.lastCreateInput.AttachmentIDs; len(got) != 1 || got[0] != attachmentA {
		t.Fatalf("expected canonical id, got %+v", got)
	}
}

// Editing does not touch attachments, and it still may not empty a message.
func TestMessageService_EditMessage_StillRequiresABody(t *testing.T) {
	msgs := &fakeMessageStore{messagesByKey: map[string]domain.Message{
		"ws-1:msg-1": {ID: "msg-1", WorkspaceID: "ws-1", ChannelID: "ch-1", SenderID: user1},
	}}
	_, err := service.NewMessageService(&fakeChannelStore{}, &fakeDMStore{}, msgs).
		EditMessage(context.Background(), service.EditMessageInput{
			WorkspaceID: "ws-1", MessageID: "msg-1", EditorID: user1, Body: "  ",
		})
	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput, got %v", err)
	}
}
