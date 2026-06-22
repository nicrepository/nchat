package service

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

const (
	// DefaultPublishTimeout caps how long a detached post-persist broadcast
	// goroutine may wait on hub or broker backpressure.
	DefaultPublishTimeout = 5 * time.Second
	// DefaultPublishQueueCapacity bounds concurrent detached broadcasts so
	// bursts cannot accumulate unobserved goroutines behind broker backpressure.
	DefaultPublishQueueCapacity = 64
)

// MessageEventPublisher publishes message lifecycle events.
// It is satisfied by *ws.Hub (via an adapter in app/) to avoid a circular import.
// All methods must be safe for concurrent use.
type MessageEventPublisher interface {
	// PublishMessageCreated broadcasts a message.created event to subscribers of
	// the given target. targetType must be "channel" or "dm". Callers must only
	// invoke this after the message has been committed to the database.
	// msg must be the full domain.Message returned by storage (including sender info).
	PublishMessageCreated(ctx context.Context, workspaceID, targetType, targetID string, msg domain.Message)
}

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
	WorkspaceID  string
	ChannelID    string
	CallerID     string
	BeforeCursor string // opaque encoded cursor; empty = most recent page
	Limit        int    // 0 = default (50); capped at 100
}

// ListDMMessagesInput identifies the DM conversation and caller for a message list.
type ListDMMessagesInput struct {
	WorkspaceID    string
	ConversationID string
	CallerID       string
	BeforeCursor   string // opaque encoded cursor; empty = most recent page
	Limit          int    // 0 = default (50); capped at 100
}

// ListChannelMessagesOutput is the result of a paginated channel message list.
// Messages are sorted oldest-first. NextCursor is non-empty when an older page exists.
type ListChannelMessagesOutput struct {
	Messages   []domain.Message
	NextCursor string
}

// ListDMMessagesOutput is the result of a paginated DM message list.
// Messages are sorted oldest-first. NextCursor is non-empty when an older page exists.
type ListDMMessagesOutput struct {
	Messages   []domain.Message
	NextCursor string
}

// GetChannelMessageInput identifies a single channel message to fetch.
type GetChannelMessageInput struct {
	WorkspaceID string
	ChannelID   string
	CallerID    string
	MessageID   string
}

// GetDMMessageInput identifies a single DM message to fetch.
type GetDMMessageInput struct {
	WorkspaceID    string
	ConversationID string
	CallerID       string
	MessageID      string
}

// MessageService handles message creation and listing for channels and DM conversations.
type MessageService struct {
	channels    storage.ChannelStore
	dms         storage.DMStore
	messages    storage.MessageStore
	publisherMu sync.RWMutex
	publisher   MessageEventPublisher // optional; nil means no broadcast

	publishSlots      chan struct{}
	droppedPublishCnt atomic.Int64
}

// NewMessageService creates a MessageService backed by the provided stores.
func NewMessageService(channels storage.ChannelStore, dms storage.DMStore, messages storage.MessageStore) *MessageService {
	return &MessageService{
		channels:     channels,
		dms:          dms,
		messages:     messages,
		publishSlots: make(chan struct{}, DefaultPublishQueueCapacity),
	}
}

// SetPublisher attaches an event publisher. Call after creating both the service
// and the hub (e.g. in app.New). Safe for concurrent use with message creation.
func (s *MessageService) SetPublisher(p MessageEventPublisher) {
	s.publisherMu.Lock()
	defer s.publisherMu.Unlock()
	s.publisher = p
}

func (s *MessageService) getPublisher() MessageEventPublisher {
	s.publisherMu.RLock()
	defer s.publisherMu.RUnlock()
	return s.publisher
}

// DroppedPublishCount returns how many post-persist broadcasts were dropped
// because the bounded async publish queue was saturated. Message persistence is
// not rolled back when this counter increments.
func (s *MessageService) DroppedPublishCount() int64 {
	return s.droppedPublishCnt.Load()
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
	s.publishMessageCreated(ctx, workspaceID, "channel", channelID, msg)
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
	s.publishMessageCreated(ctx, workspaceID, "dm", conversationID, msg)
	return msg, nil
}

func (s *MessageService) publishMessageCreated(ctx context.Context, workspaceID, targetType, targetID string, msg domain.Message) {
	publisher := s.getPublisher()
	if publisher == nil {
		return
	}

	select {
	case s.publishSlots <- struct{}{}:
	default:
		s.droppedPublishCnt.Add(1)
		return
	}

	baseCtx := context.WithoutCancel(ctx)
	go func() {
		defer func() { <-s.publishSlots }()

		publishCtx, cancel := context.WithTimeout(baseCtx, DefaultPublishTimeout)
		defer cancel()
		publisher.PublishMessageCreated(publishCtx, workspaceID, targetType, targetID, msg)
	}()
}

// ListChannelMessages returns messages for a channel visible to the caller.
// Target visibility is checked before listing so inaccessible targets return a
// non-enumerating ErrNotFound instead of a misleading empty conversation.
// BeforeCursor and Limit in the input control pagination. Returns ErrInvalidCursor
// for a non-empty cursor that cannot be decoded.
func (s *MessageService) ListChannelMessages(ctx context.Context, input ListChannelMessagesInput) (ListChannelMessagesOutput, error) {
	workspaceID := strings.TrimSpace(input.WorkspaceID)
	channelID := strings.TrimSpace(input.ChannelID)
	callerID := strings.TrimSpace(input.CallerID)
	if workspaceID == "" || channelID == "" || callerID == "" {
		return ListChannelMessagesOutput{}, fmt.Errorf("%w: workspace_id, channel_id, and caller_id are required", domain.ErrInvalidInput)
	}

	storageInput := storage.ListChannelMessagesInput{
		WorkspaceID: workspaceID,
		ChannelID:   channelID,
		UserID:      callerID,
		Limit:       input.Limit,
	}
	if input.BeforeCursor != "" {
		c, err := storage.DecodeCursor(input.BeforeCursor)
		if err != nil {
			return ListChannelMessagesOutput{}, domain.ErrInvalidCursor
		}
		storageInput.BeforeCursor = &c
	}
	if _, err := s.channels.GetVisibleChannelByID(ctx, workspaceID, channelID, callerID); err != nil {
		return ListChannelMessagesOutput{}, err
	}

	result, err := s.messages.ListChannelMessages(ctx, storageInput)
	if err != nil {
		return ListChannelMessagesOutput{}, fmt.Errorf("list channel messages: %w", err)
	}

	var nextCursor string
	if result.NextCursor != nil {
		nextCursor = storage.EncodeCursor(*result.NextCursor)
	}
	return ListChannelMessagesOutput{Messages: result.Messages, NextCursor: nextCursor}, nil
}

// ListDMMessages returns messages for a DM conversation visible to the caller.
// Target visibility is checked before listing so inaccessible targets return a
// non-enumerating ErrNotFound instead of a misleading empty conversation.
// BeforeCursor and Limit in the input control pagination. Returns ErrInvalidCursor
// for a non-empty cursor that cannot be decoded.
func (s *MessageService) ListDMMessages(ctx context.Context, input ListDMMessagesInput) (ListDMMessagesOutput, error) {
	workspaceID := strings.TrimSpace(input.WorkspaceID)
	conversationID := strings.TrimSpace(input.ConversationID)
	callerID := strings.TrimSpace(input.CallerID)
	if workspaceID == "" || conversationID == "" || callerID == "" {
		return ListDMMessagesOutput{}, fmt.Errorf("%w: workspace_id, conversation_id, and caller_id are required", domain.ErrInvalidInput)
	}

	storageInput := storage.ListDMMessagesInput{
		WorkspaceID:    workspaceID,
		ConversationID: conversationID,
		UserID:         callerID,
		Limit:          input.Limit,
	}
	if input.BeforeCursor != "" {
		c, err := storage.DecodeCursor(input.BeforeCursor)
		if err != nil {
			return ListDMMessagesOutput{}, domain.ErrInvalidCursor
		}
		storageInput.BeforeCursor = &c
	}
	if _, err := s.dms.GetVisibleConversationByID(ctx, workspaceID, conversationID, callerID); err != nil {
		return ListDMMessagesOutput{}, err
	}

	result, err := s.messages.ListDMMessages(ctx, storageInput)
	if err != nil {
		return ListDMMessagesOutput{}, fmt.Errorf("list dm messages: %w", err)
	}

	var nextCursor string
	if result.NextCursor != nil {
		nextCursor = storage.EncodeCursor(*result.NextCursor)
	}
	return ListDMMessagesOutput{Messages: result.Messages, NextCursor: nextCursor}, nil
}

// validateRefMessage validates an optional reference message ID (parent, forwarded_from,
// or referenced). Returns "" when refID is empty. Returns ErrInvalidMessageReference
// for any invalid case (invalid UUID, non-existent, cross-workspace, cross-channel,
// channel-to-DM, DM-to-channel). The error is intentionally non-enumerating: callers
// cannot determine whether the referenced message exists.
func (s *MessageService) validateRefMessage(ctx context.Context, workspaceID, channelID, dmConversationID, refID string) (string, error) {
	if refID == "" {
		return "", nil
	}
	if _, err := uuid.Parse(refID); err != nil {
		return "", domain.ErrInvalidMessageReference
	}
	if err := s.messages.ValidateRefMessageInTarget(ctx, workspaceID, channelID, dmConversationID, refID); err != nil {
		return "", err
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

// GetChannelMessage returns a single channel message visible to the caller.
// Caller must be a member with read access to the channel.
// Returns ErrNotFound when the message does not exist or belongs to a different channel.
func (s *MessageService) GetChannelMessage(ctx context.Context, input GetChannelMessageInput) (domain.Message, error) {
	workspaceID := strings.TrimSpace(input.WorkspaceID)
	channelID := strings.TrimSpace(input.ChannelID)
	callerID := strings.TrimSpace(input.CallerID)
	messageID := strings.TrimSpace(input.MessageID)
	if workspaceID == "" || channelID == "" || callerID == "" || messageID == "" {
		return domain.Message{}, fmt.Errorf("%w: workspace_id, channel_id, caller_id, and message_id are required", domain.ErrInvalidInput)
	}

	if _, err := s.channels.GetVisibleChannelByID(ctx, workspaceID, channelID, callerID); err != nil {
		return domain.Message{}, err
	}

	msg, err := s.messages.GetMessageByIDInWorkspace(ctx, workspaceID, messageID)
	if err != nil {
		return domain.Message{}, err
	}
	// Non-enumerating: a message in a different channel looks like not found.
	if msg.ChannelID != channelID {
		return domain.Message{}, domain.ErrNotFound
	}
	return msg, nil
}

// GetDMMessage returns a single DM message visible to the caller.
// Caller must be an active participant in the DM conversation.
// Returns ErrNotFound when the message does not exist or belongs to a different conversation.
func (s *MessageService) GetDMMessage(ctx context.Context, input GetDMMessageInput) (domain.Message, error) {
	workspaceID := strings.TrimSpace(input.WorkspaceID)
	conversationID := strings.TrimSpace(input.ConversationID)
	callerID := strings.TrimSpace(input.CallerID)
	messageID := strings.TrimSpace(input.MessageID)
	if workspaceID == "" || conversationID == "" || callerID == "" || messageID == "" {
		return domain.Message{}, fmt.Errorf("%w: workspace_id, conversation_id, caller_id, and message_id are required", domain.ErrInvalidInput)
	}

	if _, err := s.dms.GetVisibleConversationByID(ctx, workspaceID, conversationID, callerID); err != nil {
		return domain.Message{}, err
	}

	msg, err := s.messages.GetMessageByIDInWorkspace(ctx, workspaceID, messageID)
	if err != nil {
		return domain.Message{}, err
	}
	// Non-enumerating: a message in a different conversation looks like not found.
	if msg.DMConversationID != conversationID {
		return domain.Message{}, domain.ErrNotFound
	}
	return msg, nil
}
