package service

import (
	"context"
	"errors"
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
	// defaultMentionLabelCacheTTL is used when SetMentionLabelCacheTTL is never
	// called (e.g. in tests). Production wiring overrides it from
	// config.Config.MentionLabelCacheTTLSeconds.
	defaultMentionLabelCacheTTL = 45 * time.Second
)

// MentionLabelCache caches current mention labels for message read paths.
// Implementations must scope entries by workspaceID.
type MentionLabelCache interface {
	Get(ctx context.Context, workspaceID string, refs []string) (map[string]string, error)
	Set(ctx context.Context, workspaceID string, labels map[string]string, ttl time.Duration) error
}

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

type messageUpdatedPublisher interface {
	PublishMessageUpdated(ctx context.Context, workspaceID, targetType, targetID string, msg domain.Message)
}

const maxMessageBodyRunes = 40_000
const maxEditHistoryOffset = 10_000
const MaxMessageReferenceBatchSize = 100

// CreateChannelMessageInput is the caller-provided input for posting to a channel.
// Status, timestamps, edited_at, deleted_at, and workspace_id are not caller-settable.
type CreateChannelMessageInput struct {
	WorkspaceID     string
	ChannelID       string
	SenderID        string
	BodyText        string
	BodyFormat      domain.MessageBodyFormat
	ParentMessageID string
	// ForwardedFromMessageID remains reserved for RF-08 and is not exposed via HTTP.
	ForwardedFromMessageID string
	ReferencedMessageID    string
}

// CreateDMMessageInput is the caller-provided input for posting to a DM conversation.
// Status, timestamps, edited_at, deleted_at, and workspace_id are not caller-settable.
type CreateDMMessageInput struct {
	WorkspaceID     string
	ConversationID  string
	SenderID        string
	BodyText        string
	BodyFormat      domain.MessageBodyFormat
	ParentMessageID string
	// ForwardedFromMessageID remains reserved for RF-08 and is not exposed via HTTP.
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

// ResolveMessageReferencesInput identifies destination messages whose RF-09
// references must be re-authorized for the current caller.
type ResolveMessageReferencesInput struct {
	WorkspaceID      string
	ChannelID        string
	DMConversationID string
	CallerID         string
	MessageIDs       []string
}

type MessageReferenceResolution struct {
	MessageID string
	Reference domain.MessageReference
}

type EditMessageInput struct {
	WorkspaceID string
	MessageID   string
	EditorID    string
	Body        string
	BodyFormat  domain.MessageBodyFormat
}

type DeleteMessageInput struct {
	WorkspaceID string
	MessageID   string
	RequesterID string
}

type GetMessageEditHistoryInput struct {
	WorkspaceID string
	MessageID   string
	CallerID    string
	Limit       int
	Offset      int
}

// MessageService handles message creation and listing for channels and DM conversations.
type MessageService struct {
	channels         storage.ChannelStore
	dms              storage.DMStore
	messages         storage.MessageStore
	mentionLabels    MentionLabelCache
	mentionLabelsTTL time.Duration
	publisherMu      sync.RWMutex
	publisher        MessageEventPublisher // optional; nil means no broadcast

	publishSlots      chan struct{}
	droppedPublishCnt atomic.Int64
}

// SetMentionLabelCache enables the optional read-through cache. Configure it
// during application startup, before serving requests.
func (s *MessageService) SetMentionLabelCache(cache MentionLabelCache) {
	s.mentionLabels = cache
}

// SetMentionLabelCacheTTL overrides the cache entry lifetime (default 45s).
// Lower values propagate display-name changes and account deactivation
// ("right to be forgotten") faster, at the cost of more load on Valkey.
func (s *MessageService) SetMentionLabelCacheTTL(ttl time.Duration) {
	if ttl <= 0 {
		return
	}
	s.mentionLabelsTTL = ttl
}

// NewMessageService creates a MessageService backed by the provided stores.
func NewMessageService(channels storage.ChannelStore, dms storage.DMStore, messages storage.MessageStore) *MessageService {
	return &MessageService{
		channels:         channels,
		dms:              dms,
		messages:         messages,
		mentionLabelsTTL: defaultMentionLabelCacheTTL,
		publishSlots:     make(chan struct{}, DefaultPublishQueueCapacity),
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
	bodyFormat, err := normalizeBodyFormat(input.BodyFormat)
	if err != nil {
		return domain.Message{}, err
	}

	// SQL-enforce channel visibility: workspace active + workspace member active +
	// channel active + private-channel membership. Returns ErrNotFound for all
	// invisible targets (non-enumerating).
	if _, err := s.channels.GetVisibleChannelByID(ctx, workspaceID, channelID, senderID); err != nil {
		return domain.Message{}, err
	}

	var mentionedUserIDs, mentionedChannelIDs []string
	if bodyFormat == domain.MessageBodyFormatV3 {
		mentionedUserIDs, mentionedChannelIDs = extractMentionIDs(body)
	}
	if len(mentionedUserIDs)+len(mentionedChannelIDs) > 0 {
		labels, err := s.messages.ResolveAuthorizedMentionLabels(
			ctx, workspaceID, channelID, senderID, mentionedUserIDs, mentionedChannelIDs,
		)
		if err != nil {
			return domain.Message{}, fmt.Errorf("resolve authorized mention labels: %w", err)
		}
		if err := validateMentionRefs(mentionedUserIDs, mentionedChannelIDs, labels); err != nil {
			return domain.Message{}, err
		}
		body = rewriteMentionLabels(body, labels)
	}

	parentID, err := s.validateRefMessage(ctx, workspaceID, channelID, "", senderID, strings.TrimSpace(input.ParentMessageID))
	if err != nil {
		return domain.Message{}, err
	}
	forwardedID, err := s.validateRefMessage(ctx, workspaceID, channelID, "", senderID, strings.TrimSpace(input.ForwardedFromMessageID))
	if err != nil {
		return domain.Message{}, err
	}
	referencedID, reference, err := s.validateReferencedMessage(ctx, workspaceID, senderID, strings.TrimSpace(input.ReferencedMessageID))
	if err != nil {
		return domain.Message{}, err
	}
	if reference != nil && reference.TargetType == "channel" && reference.TargetID == channelID {
		return domain.Message{}, domain.ErrInvalidMessageReference
	}

	msg, err := s.messages.CreateMessage(ctx, storage.CreateMessageInput{
		WorkspaceID:            workspaceID,
		ChannelID:              channelID,
		SenderID:               senderID,
		Kind:                   domain.MessageKindUser,
		BodyText:               body,
		BodyFormat:             bodyFormat,
		ParentMessageID:        parentID,
		ForwardedFromMessageID: forwardedID,
		ReferencedMessageID:    referencedID,
		MentionedUserIDs:       mentionedUserIDs,
		MentionedChannelIDs:    mentionedChannelIDs,
	})
	if err != nil {
		return domain.Message{}, fmt.Errorf("create channel message: %w", err)
	}
	// The validation preview is never serialized: authorization and source state
	// may have changed while the destination message was being persisted.
	created := []domain.Message{msg}
	if err := s.resolveMessageReferences(ctx, workspaceID, senderID, created); err != nil {
		msg.Reference = &domain.MessageReference{Available: false}
	} else {
		msg = created[0]
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
	bodyFormat, err := normalizeBodyFormat(input.BodyFormat)
	if err != nil {
		return domain.Message{}, err
	}

	// SQL-enforce DM visibility: workspace active + workspace member active +
	// DM conversation active + active DM membership. Returns ErrNotFound for all
	// invisible targets (non-enumerating).
	if _, err := s.dms.GetVisibleConversationByID(ctx, workspaceID, conversationID, senderID); err != nil {
		return domain.Message{}, err
	}

	parentID, err := s.validateRefMessage(ctx, workspaceID, "", conversationID, senderID, strings.TrimSpace(input.ParentMessageID))
	if err != nil {
		return domain.Message{}, err
	}
	forwardedID, err := s.validateRefMessage(ctx, workspaceID, "", conversationID, senderID, strings.TrimSpace(input.ForwardedFromMessageID))
	if err != nil {
		return domain.Message{}, err
	}
	referencedID, reference, err := s.validateReferencedMessage(ctx, workspaceID, senderID, strings.TrimSpace(input.ReferencedMessageID))
	if err != nil {
		return domain.Message{}, err
	}
	if reference != nil && reference.TargetType == "dm" && reference.TargetID == conversationID {
		return domain.Message{}, domain.ErrInvalidMessageReference
	}

	msg, err := s.messages.CreateMessage(ctx, storage.CreateMessageInput{
		WorkspaceID:            workspaceID,
		DMConversationID:       conversationID,
		SenderID:               senderID,
		Kind:                   domain.MessageKindUser,
		BodyText:               body,
		BodyFormat:             bodyFormat,
		ParentMessageID:        parentID,
		ForwardedFromMessageID: forwardedID,
		ReferencedMessageID:    referencedID,
	})
	if err != nil {
		return domain.Message{}, fmt.Errorf("create dm message: %w", err)
	}
	// Resolve again after persistence so the HTTP response never reuses the
	// preview obtained during pre-validation.
	created := []domain.Message{msg}
	if err := s.resolveMessageReferences(ctx, workspaceID, senderID, created); err != nil {
		msg.Reference = &domain.MessageReference{Available: false}
	} else {
		msg = created[0]
	}
	s.publishMessageCreated(ctx, workspaceID, "dm", conversationID, msg)
	return msg, nil
}

func (s *MessageService) publishMessageCreated(ctx context.Context, workspaceID, targetType, targetID string, msg domain.Message) {
	publisher := s.getPublisher()
	if publisher == nil {
		return
	}
	s.enqueuePublish(ctx, func(publishCtx context.Context) {
		publisher.PublishMessageCreated(publishCtx, workspaceID, targetType, targetID, msg)
	})
}

func (s *MessageService) publishMessageUpdated(ctx context.Context, msg domain.Message) {
	publisher, ok := s.getPublisher().(messageUpdatedPublisher)
	if !ok {
		return
	}
	targetType, targetID := "channel", msg.ChannelID
	if msg.DMConversationID != "" {
		targetType, targetID = "dm", msg.DMConversationID
	}
	s.enqueuePublish(ctx, func(publishCtx context.Context) {
		publisher.PublishMessageUpdated(publishCtx, msg.WorkspaceID, targetType, targetID, msg)
	})
}

func (s *MessageService) enqueuePublish(ctx context.Context, publish func(context.Context)) {
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
		publish(publishCtx)
	}()
}

// EditMessage validates the body with the creation path's rules, then delegates
// the atomic authorization, snapshot, window check, and update to storage.
func (s *MessageService) EditMessage(ctx context.Context, input EditMessageInput) (domain.Message, error) {
	workspaceID := strings.TrimSpace(input.WorkspaceID)
	messageID := strings.TrimSpace(input.MessageID)
	editorID := strings.TrimSpace(input.EditorID)
	body := strings.TrimSpace(input.Body)
	if workspaceID == "" || messageID == "" || editorID == "" {
		return domain.Message{}, fmt.Errorf("%w: workspace_id, message_id, and editor_id are required", domain.ErrInvalidInput)
	}
	if err := validateMessageBody(body); err != nil {
		return domain.Message{}, err
	}
	bodyFormat, err := normalizeBodyFormat(input.BodyFormat)
	if err != nil {
		return domain.Message{}, err
	}

	current, err := s.messages.GetMessageByIDInWorkspace(ctx, workspaceID, messageID, editorID)
	if err != nil {
		return domain.Message{}, err
	}
	body, err = s.resolveAndRewriteMentions(ctx, workspaceID, current.ChannelID, editorID, body, bodyFormat)
	if err != nil {
		return domain.Message{}, err
	}

	updated, err := s.messages.EditMessage(ctx, storage.EditMessageInput{
		WorkspaceID: workspaceID, MessageID: messageID, EditorID: editorID,
		Body: body, BodyFormat: bodyFormat,
	})
	if err != nil {
		return domain.Message{}, fmt.Errorf("edit message: %w", err)
	}
	updatedMessages := []domain.Message{updated}
	if err := s.resolveMessageReferences(ctx, workspaceID, editorID, updatedMessages); err != nil {
		return domain.Message{}, err
	}
	updated = updatedMessages[0]
	s.publishMessageUpdated(ctx, updated)
	return updated, nil
}

// DeleteMessage soft-deletes an authored user message and publishes the
// sanitized placeholder only after the transaction commits.
func (s *MessageService) DeleteMessage(ctx context.Context, input DeleteMessageInput) (domain.Message, error) {
	workspaceID := strings.TrimSpace(input.WorkspaceID)
	messageID := strings.TrimSpace(input.MessageID)
	requesterID := strings.TrimSpace(input.RequesterID)
	if workspaceID == "" || messageID == "" || requesterID == "" {
		return domain.Message{}, fmt.Errorf("%w: workspace_id, message_id, and requester_id are required", domain.ErrInvalidInput)
	}

	deleted, changed, err := s.messages.DeleteMessage(ctx, storage.DeleteMessageInput{
		WorkspaceID: workspaceID, MessageID: messageID, RequesterID: requesterID,
	})
	if err != nil {
		// Do not expose whether a readable message belongs to another author (or
		// is a non-deletable system message) through a distinct HTTP status.
		if errors.Is(err, domain.ErrForbidden) {
			return domain.Message{}, domain.ErrNotFound
		}
		return domain.Message{}, fmt.Errorf("delete message: %w", err)
	}
	// The database retains the body for future retention/audit policy, but no
	// normal service response or event may carry it after deletion.
	deleted.BodyText = ""
	deleted.Quoted = nil
	if changed {
		s.publishMessageUpdated(ctx, deleted)
	}
	return deleted, nil
}

func (s *MessageService) resolveAndRewriteMentions(ctx context.Context, workspaceID, channelID, requesterID, body string, bodyFormat domain.MessageBodyFormat) (string, error) {
	if bodyFormat != domain.MessageBodyFormatV3 || channelID == "" {
		return body, nil
	}
	userIDs, channelIDs := extractMentionIDs(body)
	if len(userIDs)+len(channelIDs) == 0 {
		return body, nil
	}
	labels, err := s.messages.ResolveAuthorizedMentionLabels(
		ctx, workspaceID, channelID, requesterID, userIDs, channelIDs,
	)
	if err != nil {
		return "", fmt.Errorf("resolve authorized mention labels: %w", err)
	}
	if err := validateMentionRefs(userIDs, channelIDs, labels); err != nil {
		return "", err
	}
	return rewriteMentionLabels(body, labels), nil
}

func (s *MessageService) GetMessageEditHistory(ctx context.Context, input GetMessageEditHistoryInput) ([]domain.MessageEditHistory, error) {
	workspaceID := strings.TrimSpace(input.WorkspaceID)
	messageID := strings.TrimSpace(input.MessageID)
	callerID := strings.TrimSpace(input.CallerID)
	if workspaceID == "" || messageID == "" || callerID == "" || input.Offset < 0 || input.Offset > maxEditHistoryOffset {
		return nil, fmt.Errorf("%w: invalid history request", domain.ErrInvalidInput)
	}
	return s.messages.ListMessageEditHistory(ctx, storage.ListMessageEditHistoryInput{
		WorkspaceID: workspaceID, MessageID: messageID, UserID: callerID,
		Limit: input.Limit, Offset: input.Offset,
	})
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
	if err := s.refreshMentionLabels(ctx, workspaceID, result.Messages); err != nil {
		return ListChannelMessagesOutput{}, err
	}
	if err := s.resolveMessageReferences(ctx, workspaceID, callerID, result.Messages); err != nil {
		return ListChannelMessagesOutput{}, err
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
	if err := s.resolveMessageReferences(ctx, workspaceID, callerID, result.Messages); err != nil {
		return ListDMMessagesOutput{}, err
	}

	var nextCursor string
	if result.NextCursor != nil {
		nextCursor = storage.EncodeCursor(*result.NextCursor)
	}
	return ListDMMessagesOutput{Messages: result.Messages, NextCursor: nextCursor}, nil
}

// validateRefMessage validates the same-target IDs used by RF-07 and reserved RF-08.
func (s *MessageService) validateRefMessage(ctx context.Context, workspaceID, channelID, dmConversationID, senderID, refID string) (string, error) {
	if refID == "" {
		return "", nil
	}
	if _, err := uuid.Parse(refID); err != nil {
		return "", domain.ErrInvalidMessageReference
	}
	if err := s.messages.ValidateRefMessageInTarget(ctx, workspaceID, channelID, dmConversationID, refID, senderID); err != nil {
		return "", err
	}
	return refID, nil
}

func (s *MessageService) validateReferencedMessage(ctx context.Context, workspaceID, senderID, refID string) (string, *domain.MessageReference, error) {
	if refID == "" {
		return "", nil, nil
	}
	parsed, err := uuid.Parse(refID)
	if err != nil {
		return "", nil, domain.ErrInvalidMessageReference
	}
	refID = parsed.String()
	resolved, err := s.messages.ResolveMessageReferences(ctx, workspaceID, senderID, []string{refID})
	if err != nil {
		return "", nil, fmt.Errorf("resolve referenced message: %w", err)
	}
	ref, ok := resolved[refID]
	if !ok {
		return "", nil, domain.ErrInvalidMessageReference
	}
	return refID, &ref, nil
}

func (s *MessageService) resolveMessageReferences(ctx context.Context, workspaceID, userID string, messages []domain.Message) error {
	ids := make([]string, 0)
	for i := range messages {
		if messages[i].ReferencedMessageID == "" {
			continue
		}
		messages[i].Reference = &domain.MessageReference{Available: false}
		ids = append(ids, messages[i].ReferencedMessageID)
	}
	if len(ids) == 0 {
		return nil
	}
	resolved, err := s.messages.ResolveMessageReferences(ctx, workspaceID, userID, uniqueStrings(ids))
	if err != nil {
		return fmt.Errorf("resolve message references: %w", err)
	}
	for i := range messages {
		if ref, ok := resolved[messages[i].ReferencedMessageID]; ok {
			copy := ref
			messages[i].Reference = &copy
		}
	}
	return nil
}

// ResolveMessageReferenceBatch re-authorizes RF-09 origins for a bounded set
// of destination messages. Missing, invalid, inaccessible, and unreferenced
// destinations all receive the same unavailable result.
func (s *MessageService) ResolveMessageReferenceBatch(ctx context.Context, input ResolveMessageReferencesInput) ([]MessageReferenceResolution, error) {
	workspaceID := strings.TrimSpace(input.WorkspaceID)
	callerID := strings.TrimSpace(input.CallerID)
	channelID := strings.TrimSpace(input.ChannelID)
	dmConversationID := strings.TrimSpace(input.DMConversationID)
	if workspaceID == "" || callerID == "" || (channelID == "") == (dmConversationID == "") {
		return nil, fmt.Errorf("%w: workspace_id, caller_id, and exactly one target are required", domain.ErrInvalidInput)
	}
	if len(input.MessageIDs) == 0 || len(input.MessageIDs) > MaxMessageReferenceBatchSize {
		return nil, fmt.Errorf("%w: message_ids must contain 1-%d values", domain.ErrInvalidInput, MaxMessageReferenceBatchSize)
	}

	messageIDs := make([]string, 0, len(input.MessageIDs))
	seen := make(map[string]struct{}, len(input.MessageIDs))
	for _, rawID := range input.MessageIDs {
		parsed, err := uuid.Parse(strings.TrimSpace(rawID))
		if err != nil {
			return nil, fmt.Errorf("%w: invalid message id", domain.ErrInvalidInput)
		}
		id := parsed.String()
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		messageIDs = append(messageIDs, id)
	}

	if channelID != "" {
		if _, err := s.channels.GetVisibleChannelByID(ctx, workspaceID, channelID, callerID); err != nil {
			return nil, err
		}
	} else if _, err := s.dms.GetVisibleConversationByID(ctx, workspaceID, dmConversationID, callerID); err != nil {
		return nil, err
	}

	referencedIDs, err := s.messages.ListReferencedMessageIDs(ctx, workspaceID, callerID, channelID, dmConversationID, messageIDs)
	if err != nil {
		return nil, fmt.Errorf("list destination references: %w", err)
	}
	sourceIDs := make([]string, 0, len(referencedIDs))
	for _, sourceID := range referencedIDs {
		sourceIDs = append(sourceIDs, sourceID)
	}
	resolved, err := s.messages.ResolveMessageReferences(ctx, workspaceID, callerID, uniqueStrings(sourceIDs))
	if err != nil {
		return nil, fmt.Errorf("resolve destination references: %w", err)
	}

	result := make([]MessageReferenceResolution, 0, len(messageIDs))
	for _, destinationID := range messageIDs {
		ref := domain.MessageReference{Available: false}
		if sourceID := referencedIDs[destinationID]; sourceID != "" {
			if available, ok := resolved[sourceID]; ok {
				ref = available
			}
		}
		result = append(result, MessageReferenceResolution{MessageID: destinationID, Reference: ref})
	}
	return result, nil
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

func validateMentionRefs(userIDs, channelIDs []string, labels map[string]string) error {
	for _, id := range userIDs {
		if _, ok := labels["user:"+id]; !ok {
			return fmt.Errorf("%w: invalid mention", domain.ErrInvalidInput)
		}
	}
	for _, id := range channelIDs {
		if _, ok := labels["channel:"+id]; !ok {
			return fmt.Errorf("%w: invalid mention", domain.ErrInvalidInput)
		}
	}
	return nil
}

func normalizeBodyFormat(format domain.MessageBodyFormat) (domain.MessageBodyFormat, error) {
	if format == "" {
		return domain.MessageBodyFormatV1, nil
	}
	if format != domain.MessageBodyFormatV1 && format != domain.MessageBodyFormatV2 && format != domain.MessageBodyFormatV3 {
		return "", fmt.Errorf("%w: unsupported body_format", domain.ErrInvalidInput)
	}
	return format, nil
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

	msg, err := s.messages.GetMessageByIDInWorkspace(ctx, workspaceID, messageID, callerID)
	if err != nil {
		return domain.Message{}, err
	}
	// Non-enumerating: a message in a different channel looks like not found.
	if msg.ChannelID != channelID {
		return domain.Message{}, domain.ErrNotFound
	}
	messages := []domain.Message{msg}
	if err := s.refreshMentionLabels(ctx, workspaceID, messages); err != nil {
		return domain.Message{}, err
	}
	if err := s.resolveMessageReferences(ctx, workspaceID, callerID, messages); err != nil {
		return domain.Message{}, err
	}
	return messages[0], nil
}

func (s *MessageService) refreshMentionLabels(ctx context.Context, workspaceID string, messages []domain.Message) error {
	userIDs, channelIDs := []string{}, []string{}
	for _, msg := range messages {
		if msg.BodyFormat != domain.MessageBodyFormatV3 {
			continue
		}
		users, channels := extractMentionIDs(msg.BodyText)
		userIDs = append(userIDs, users...)
		channelIDs = append(channelIDs, channels...)
	}
	if len(userIDs)+len(channelIDs) == 0 {
		return nil
	}
	userIDs = uniqueStrings(userIDs)
	channelIDs = uniqueStrings(channelIDs)
	refs := make([]string, 0, len(userIDs)+len(channelIDs))
	for _, id := range userIDs {
		refs = append(refs, "user:"+id)
	}
	for _, id := range channelIDs {
		refs = append(refs, "channel:"+id)
	}

	labels := make(map[string]string, len(refs))
	if s.mentionLabels != nil {
		cached, err := s.mentionLabels.Get(ctx, workspaceID, refs)
		if err == nil {
			for ref, label := range cached {
				labels[ref] = label
			}
		}
	}

	missingUsers, missingChannels := []string{}, []string{}
	for _, id := range userIDs {
		if _, ok := labels["user:"+id]; !ok {
			missingUsers = append(missingUsers, id)
		}
	}
	for _, id := range channelIDs {
		if _, ok := labels["channel:"+id]; !ok {
			missingChannels = append(missingChannels, id)
		}
	}
	if len(missingUsers)+len(missingChannels) > 0 {
		resolved, err := s.messages.ResolveMentionLabels(ctx, workspaceID, missingUsers, missingChannels)
		if err != nil {
			return fmt.Errorf("resolve mention labels: %w", err)
		}
		for ref, label := range resolved {
			labels[ref] = label
		}
		if s.mentionLabels != nil && len(resolved) > 0 {
			_ = s.mentionLabels.Set(ctx, workspaceID, resolved, s.mentionLabelsTTL)
		}
	}
	for i := range messages {
		if messages[i].BodyFormat == domain.MessageBodyFormatV3 {
			messages[i].BodyText = rewriteMentionLabels(messages[i].BodyText, labels)
		}
	}
	return nil
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	unique := make([]string, 0, len(values))
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		unique = append(unique, value)
	}
	return unique
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

	msg, err := s.messages.GetMessageByIDInWorkspace(ctx, workspaceID, messageID, callerID)
	if err != nil {
		return domain.Message{}, err
	}
	// Non-enumerating: a message in a different conversation looks like not found.
	if msg.DMConversationID != conversationID {
		return domain.Message{}, domain.ErrNotFound
	}
	messages := []domain.Message{msg}
	if err := s.resolveMessageReferences(ctx, workspaceID, callerID, messages); err != nil {
		return domain.Message{}, err
	}
	msg = messages[0]
	return msg, nil
}
