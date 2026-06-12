package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

const maxMessageBodyRunes = 40_000

// CreateChannelMessageInput is the caller-provided input for posting to a channel.
// Status, timestamps, edited_at, deleted_at, and workspace_id are not caller-settable.
type CreateChannelMessageInput struct {
	WorkspaceID            string
	ChannelID              string
	SenderID               string
	BodyText               string
	ParentMessageID        string
	ForwardedFromMessageID string
	ReferencedMessageID    string
}

// CreateDMMessageInput is the caller-provided input for posting to a DM conversation.
// Status, timestamps, edited_at, deleted_at, and workspace_id are not caller-settable.
type CreateDMMessageInput struct {
	WorkspaceID            string
	ConversationID         string
	SenderID               string
	BodyText               string
	ParentMessageID        string
	ForwardedFromMessageID string
	ReferencedMessageID    string
}

// ListChannelMessagesInput identifies the channel and caller for a message list.
type ListChannelMessagesInput struct {
	WorkspaceID string
	ChannelID   string
	CallerID    string
}

// ListDMMessagesInput identifies the DM conversation and caller for a message list.
type ListDMMessagesInput struct {
	WorkspaceID    string
	ConversationID string
	CallerID       string
}

// MessageService handles message creation and listing for channels and DM conversations.
type MessageService struct {
	channels storage.ChannelStore
	dms      storage.DMStore
	messages storage.MessageStore
}

// NewMessageService creates a MessageService backed by the provided stores.
func NewMessageService(channels storage.ChannelStore, dms storage.DMStore, messages storage.MessageStore) *MessageService {
	return &MessageService{channels: channels, dms: dms, messages: messages}
}

// CreateChannelMessage posts a message to a channel.
// The sender must be an active workspace member visible to the channel.
// Private channels additionally require active channel membership.
// Archived channels are denied. Cross-workspace targets are denied.
// The caller cannot set status, timestamps, edited_at, or deleted_at.
func (s *MessageService) CreateChannelMessage(ctx context.Context, input CreateChannelMessageInput) (domain.Message, error) {
	workspaceID := strings.TrimSpace(input.WorkspaceID)
	channelID := strings.TrimSpace(input.ChannelID)
	senderID := strings.TrimSpace(input.SenderID)
	body := strings.TrimSpace(input.BodyText)

	if workspaceID == "" || channelID == "" || senderID == "" {
		return domain.Message{}, fmt.Errorf("%w: workspace_id, channel_id, and sender_id are required", domain.ErrInvalidInput)
	}
	if err := validateMessageBody(body); err != nil {
		return domain.Message{}, err
	}

	// SQL-enforce channel visibility: workspace active + workspace member active +
	// channel active + private-channel membership. Returns ErrNotFound for all
	// invisible targets (non-enumerating).
	if _, err := s.channels.GetVisibleChannelByID(ctx, workspaceID, channelID, senderID); err != nil {
		return domain.Message{}, err
	}

	parentID, err := s.validateRefMessage(ctx, workspaceID, channelID, "", strings.TrimSpace(input.ParentMessageID))
	if err != nil {
		return domain.Message{}, err
	}
	forwardedID, err := s.validateRefMessage(ctx, workspaceID, channelID, "", strings.TrimSpace(input.ForwardedFromMessageID))
	if err != nil {
		return domain.Message{}, err
	}
	referencedID, err := s.validateRefMessage(ctx, workspaceID, channelID, "", strings.TrimSpace(input.ReferencedMessageID))
	if err != nil {
		return domain.Message{}, err
	}

	msg, err := s.messages.CreateMessage(ctx, storage.CreateMessageInput{
		WorkspaceID:            workspaceID,
		ChannelID:              channelID,
		SenderID:               senderID,
		Kind:                   domain.MessageKindUser,
		BodyText:               body,
		ParentMessageID:        parentID,
		ForwardedFromMessageID: forwardedID,
		ReferencedMessageID:    referencedID,
	})
	if err != nil {
		return domain.Message{}, fmt.Errorf("create channel message: %w", err)
	}
	return msg, nil
}

// CreateDMMessage posts a message to a DM conversation.
// The sender must be an active workspace member and an active DM participant.
// Archived DM conversations are denied. Cross-workspace targets are denied.
// The caller cannot set status, timestamps, edited_at, or deleted_at.
func (s *MessageService) CreateDMMessage(ctx context.Context, input CreateDMMessageInput) (domain.Message, error) {
	workspaceID := strings.TrimSpace(input.WorkspaceID)
	conversationID := strings.TrimSpace(input.ConversationID)
	senderID := strings.TrimSpace(input.SenderID)
	body := strings.TrimSpace(input.BodyText)

	if workspaceID == "" || conversationID == "" || senderID == "" {
		return domain.Message{}, fmt.Errorf("%w: workspace_id, conversation_id, and sender_id are required", domain.ErrInvalidInput)
	}
	if err := validateMessageBody(body); err != nil {
		return domain.Message{}, err
	}

	// SQL-enforce DM visibility: workspace active + workspace member active +
	// DM conversation active + active DM membership. Returns ErrNotFound for all
	// invisible targets (non-enumerating).
	if _, err := s.dms.GetVisibleConversationByID(ctx, workspaceID, conversationID, senderID); err != nil {
		return domain.Message{}, err
	}

	parentID, err := s.validateRefMessage(ctx, workspaceID, "", conversationID, strings.TrimSpace(input.ParentMessageID))
	if err != nil {
		return domain.Message{}, err
	}
	forwardedID, err := s.validateRefMessage(ctx, workspaceID, "", conversationID, strings.TrimSpace(input.ForwardedFromMessageID))
	if err != nil {
		return domain.Message{}, err
	}
	referencedID, err := s.validateRefMessage(ctx, workspaceID, "", conversationID, strings.TrimSpace(input.ReferencedMessageID))
	if err != nil {
		return domain.Message{}, err
	}

	msg, err := s.messages.CreateMessage(ctx, storage.CreateMessageInput{
		WorkspaceID:            workspaceID,
		DMConversationID:       conversationID,
		SenderID:               senderID,
		Kind:                   domain.MessageKindUser,
		BodyText:               body,
		ParentMessageID:        parentID,
		ForwardedFromMessageID: forwardedID,
		ReferencedMessageID:    referencedID,
	})
	if err != nil {
		return domain.Message{}, fmt.Errorf("create dm message: %w", err)
	}
	return msg, nil
}

// ListChannelMessages returns messages for a channel visible to the caller.
// Visibility is enforced in SQL; an empty slice is returned for inaccessible channels.
func (s *MessageService) ListChannelMessages(ctx context.Context, input ListChannelMessagesInput) ([]domain.Message, error) {
	workspaceID := strings.TrimSpace(input.WorkspaceID)
	channelID := strings.TrimSpace(input.ChannelID)
	callerID := strings.TrimSpace(input.CallerID)
	if workspaceID == "" || channelID == "" || callerID == "" {
		return nil, fmt.Errorf("%w: workspace_id, channel_id, and caller_id are required", domain.ErrInvalidInput)
	}
	msgs, err := s.messages.ListChannelMessages(ctx, storage.ListChannelMessagesInput{
		WorkspaceID: workspaceID,
		ChannelID:   channelID,
		UserID:      callerID,
	})
	if err != nil {
		return nil, fmt.Errorf("list channel messages: %w", err)
	}
	return msgs, nil
}

// ListDMMessages returns messages for a DM conversation visible to the caller.
// Visibility is enforced in SQL; an empty slice is returned for inaccessible conversations.
func (s *MessageService) ListDMMessages(ctx context.Context, input ListDMMessagesInput) ([]domain.Message, error) {
	workspaceID := strings.TrimSpace(input.WorkspaceID)
	conversationID := strings.TrimSpace(input.ConversationID)
	callerID := strings.TrimSpace(input.CallerID)
	if workspaceID == "" || conversationID == "" || callerID == "" {
		return nil, fmt.Errorf("%w: workspace_id, conversation_id, and caller_id are required", domain.ErrInvalidInput)
	}
	msgs, err := s.messages.ListDMMessages(ctx, storage.ListDMMessagesInput{
		WorkspaceID:    workspaceID,
		ConversationID: conversationID,
		UserID:         callerID,
	})
	if err != nil {
		return nil, fmt.Errorf("list dm messages: %w", err)
	}
	return msgs, nil
}

// validateRefMessage validates an optional reference message ID (parent, forwarded_from,
// or referenced). If refID is empty, it is a no-op. Otherwise it verifies the message
// exists in the same workspace and the same target (channel or DM conversation).
// Returns the trimmed ID or "" if empty.
func (s *MessageService) validateRefMessage(ctx context.Context, workspaceID, channelID, dmConversationID, refID string) (string, error) {
	if refID == "" {
		return "", nil
	}
	if _, err := uuid.Parse(refID); err != nil {
		return "", fmt.Errorf("%w: reference message id is not a valid UUID", domain.ErrInvalidInput)
	}
	ref, err := s.messages.GetMessageByIDInWorkspace(ctx, workspaceID, refID)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return "", fmt.Errorf("%w: reference message not found in workspace", domain.ErrInvalidInput)
		}
		return "", fmt.Errorf("validate reference message: %w", err)
	}
	if channelID != "" && ref.ChannelID != channelID {
		return "", fmt.Errorf("%w: reference message must be in the same channel", domain.ErrInvalidInput)
	}
	if dmConversationID != "" && ref.DMConversationID != dmConversationID {
		return "", fmt.Errorf("%w: reference message must be in the same DM conversation", domain.ErrInvalidInput)
	}
	return refID, nil
}

func validateMessageBody(body string) error {
	if body == "" {
		return fmt.Errorf("%w: body_text is required", domain.ErrInvalidInput)
	}
	if len([]rune(body)) > maxMessageBodyRunes {
		return fmt.Errorf("%w: body_text exceeds maximum length of %d characters", domain.ErrInvalidInput, maxMessageBodyRunes)
	}
	return nil
}
