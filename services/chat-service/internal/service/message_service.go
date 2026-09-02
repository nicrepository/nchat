package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/nicrepository/nchat/libs/go/platform/urlsafety"
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
const MaxMessageSecuritySnapshotBatchSize = 100

// MaxLinkSafetyStatusBatchSize bounds one reconnect reconciliation.
//
// The same ceiling as a reference batch, because it is the same kind of request:
// a client asking about the ids on one screen. A client holds a withheld message
// only between sending it and its scan resolving, so a hundred at once is already
// far more than the flow produces — the bound is here so a caller cannot turn a
// reconnect into an arbitrarily large query.
const MaxLinkSafetyStatusBatchSize = 100

// normalizeMessageIDBatch validates and canonicalises a batch of message ids.
//
// Shared by every endpoint that takes a list of ids from a client: it bounds the
// batch, rejects anything that is not a UUID before a query runs, and collapses
// duplicates so a caller cannot multiply the work by repeating one id.
func normalizeMessageIDBatch(rawIDs []string, maxSize int) ([]string, error) {
	if len(rawIDs) == 0 || len(rawIDs) > maxSize {
		return nil, fmt.Errorf("%w: message_ids must contain 1-%d values", domain.ErrInvalidInput, maxSize)
	}
	ids := make([]string, 0, len(rawIDs))
	seen := make(map[string]struct{}, len(rawIDs))
	for _, rawID := range rawIDs {
		parsed, err := uuid.Parse(strings.TrimSpace(rawID))
		if err != nil {
			return nil, fmt.Errorf("%w: invalid message id", domain.ErrInvalidInput)
		}
		id := parsed.String()
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	return ids, nil
}

// LinkSafetyStatusInput asks what became of messages the caller sent.
type LinkSafetyStatusInput struct {
	WorkspaceID string
	SenderID    string
	MessageIDs  []string
}

// MessageSecuritySnapshot is the minimum authoritative projection needed to
// repair a visible timeline after a missed link-safety event. Bodies, URLs and
// scan identifiers never leave this endpoint.
type MessageSecuritySnapshot struct {
	MessageID       string
	Available       bool
	Status          domain.MessageStatus
	LinkSafetyState domain.MessageLinkSafety
	UpdatedAt       time.Time
	Quoted          *QuotedMessageSecuritySnapshot
}

type QuotedMessageSecuritySnapshot struct {
	MessageID       string
	Status          domain.MessageStatus
	LinkSafetyState domain.MessageLinkSafety
	UpdatedAt       time.Time
}

type MessageSecuritySnapshotsInput struct {
	WorkspaceID      string
	ChannelID        string
	DMConversationID string
	CallerID         string
	MessageIDs       []string
}

// MessageSecuritySnapshots re-authorizes one visible target and returns only
// the security axes for requested messages. Invisible, missing and wrong-target
// ids all return the same Available=false sentinel, so the client can withdraw
// stale content without the batch becoming an existence oracle.
func (s *MessageService) MessageSecuritySnapshots(
	ctx context.Context, input MessageSecuritySnapshotsInput,
) ([]MessageSecuritySnapshot, error) {
	workspaceID := strings.TrimSpace(input.WorkspaceID)
	callerID := strings.TrimSpace(input.CallerID)
	channelID := strings.TrimSpace(input.ChannelID)
	dmConversationID := strings.TrimSpace(input.DMConversationID)
	if workspaceID == "" || callerID == "" || (channelID == "") == (dmConversationID == "") {
		return nil, fmt.Errorf("%w: workspace_id, caller_id, and exactly one target are required", domain.ErrInvalidInput)
	}
	messageIDs, err := normalizeMessageIDBatch(input.MessageIDs, MaxMessageSecuritySnapshotBatchSize)
	if err != nil {
		return nil, err
	}
	if err := s.authorizeTarget(ctx, workspaceID, channelID, dmConversationID, callerID); err != nil {
		return nil, err
	}

	stored, err := s.messages.ListMessageSecuritySnapshots(
		ctx, workspaceID, callerID, channelID, dmConversationID, messageIDs,
	)
	if err != nil {
		return nil, fmt.Errorf("list message security snapshots: %w", err)
	}
	result := make([]MessageSecuritySnapshot, 0, len(stored))
	for _, message := range stored {
		snapshot := MessageSecuritySnapshot{
			MessageID: message.MessageID, Available: message.Available,
			Status: message.Status, LinkSafetyState: message.LinkSafetyState, UpdatedAt: message.UpdatedAt,
		}
		if message.Available && message.Quoted != nil {
			snapshot.Quoted = &QuotedMessageSecuritySnapshot{
				MessageID: message.Quoted.MessageID, Status: message.Quoted.Status,
				LinkSafetyState: message.Quoted.LinkSafetyState, UpdatedAt: message.Quoted.UpdatedAt,
			}
		}
		result = append(result, snapshot)
	}
	return result, nil
}

// MessageLinkSafetyStates reports the authoritative state of the caller's own
// withheld messages (RF-21).
//
// This is the client's recovery path, not an alternative to realtime. A verdict
// is announced over the websocket as it happens; this exists because that
// announcement is best-effort and an author whose connection was down while
// their message was refused would otherwise wait on an event that already came
// and went.
//
// It answers only about messages the caller wrote, and answers with a state and
// nothing else: no body, no URL, no verdict detail, no scan identifier. An id it
// will not talk about is absent from the result rather than reported as denied.
func (s *MessageService) MessageLinkSafetyStates(
	ctx context.Context, input LinkSafetyStatusInput,
) ([]domain.MessageLinkSafetyState, error) {
	workspaceID := strings.TrimSpace(input.WorkspaceID)
	senderID := strings.TrimSpace(input.SenderID)
	if workspaceID == "" || senderID == "" {
		return nil, fmt.Errorf("%w: workspace_id and sender_id are required", domain.ErrInvalidInput)
	}
	messageIDs, err := normalizeMessageIDBatch(input.MessageIDs, MaxLinkSafetyStatusBatchSize)
	if err != nil {
		return nil, err
	}
	states, err := s.messages.LinkSafetyStates(ctx, workspaceID, senderID, messageIDs)
	if err != nil {
		return nil, fmt.Errorf("read link safety states: %w", err)
	}
	return states, nil
}

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
	// AttachmentIDs are candidate files.attachments ids supplied by the client
	// (RF-32). Everything about them is re-derived server-side; see
	// normalizeAttachmentIDs and the storage layer's invalid_attachments CTE.
	AttachmentIDs []string
	// IdempotencyKey makes a retried send return the original message. Optional,
	// and supplied by the client through the Idempotency-Key header — the same
	// contract forwarding already uses.
	IdempotencyKey string
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
	// AttachmentIDs are candidate files.attachments ids supplied by the client
	// (RF-32), validated exactly as the channel path validates them.
	AttachmentIDs []string
	// IdempotencyKey makes a retried send return the original message, exactly as
	// on the channel path.
	IdempotencyKey string
}

type ForwardChannelMessageInput struct {
	WorkspaceID          string
	DestinationChannelID string
	ActorID              string
	SourceMessageID      string
	IdempotencyKey       string
}

type ForwardChannelMessageOutput struct {
	Message  domain.Message
	Replayed bool
	// Pending is true when the forwarded snapshot carries a link with no verdict
	// yet (RF-21). The message exists and the caller may report it, but nobody
	// else has been shown it.
	Pending bool
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

	// linkSafety is the RF-21 gate. Nil means the deployment did not enable the
	// check and every path behaves exactly as it did before it existed.
	linkSafety URLSafetyChecker
	// linkScanCapacity is what a workspace, and the deployment, may spend on new
	// provider work. Zero values disable the corresponding ceiling.
	linkScanCapacity storage.LinkScanCapacity
	// admissionMetrics reports capacity decisions. Nil is the no-op reporter.
	admissionMetrics          *urlsafety.PipelineMetrics
	maxMessageAttachments     int
	maxMessageAttachmentBytes int64
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
		channels:                  channels,
		dms:                       dms,
		messages:                  messages,
		mentionLabelsTTL:          defaultMentionLabelCacheTTL,
		publishSlots:              make(chan struct{}, DefaultPublishQueueCapacity),
		maxMessageAttachments:     domain.MaxMessageAttachments,
		maxMessageAttachmentBytes: domain.DefaultMaxMessageAttachmentBytes,
	}
}

func (s *MessageService) WithMessageAttachmentLimits(count int, bytes int64) *MessageService {
	if count > 0 && count <= domain.MaxMessageAttachments {
		s.maxMessageAttachments = count
	}
	if bytes > 0 {
		s.maxMessageAttachmentBytes = bytes
	}
	return s
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
	request, err := normalizeCreateRequest(createRequestInput{
		WorkspaceID: workspaceID, TargetID: input.ChannelID, TargetField: "channel_id",
		SenderID: input.SenderID, BodyText: input.BodyText,
		BodyFormat: input.BodyFormat, AttachmentIDs: input.AttachmentIDs,
	}, s.maxMessageAttachments)
	if err != nil {
		return domain.Message{}, err
	}
	channelID, senderID := request.TargetID, request.SenderID
	body, bodyFormat, attachmentIDs := request.Body, request.BodyFormat, request.AttachmentIDs

	// SQL-enforce channel visibility: workspace active + workspace member active +
	// channel active + private-channel membership. Returns ErrNotFound for all
	// invisible targets (non-enumerating).
	if _, err := s.channels.GetVisibleChannelByID(ctx, workspaceID, channelID, senderID); err != nil {
		return domain.Message{}, err
	}
	// RF-21, after authorization and before anything is written. After, so an
	// unauthorized caller cannot spend provider quota on a channel they cannot
	// post to; before, so a refusal leaves no row, no attachment binding and no
	// broadcast — the publish below is reached only by a message that committed.
	// Idempotency first, before anything external: a retry asks for the message
	// that already exists, not for a second one.
	replayInput := channelReplayInput(workspaceID, channelID, senderID, body, bodyFormat, attachmentIDs, input)
	if existing, replayed, err := s.resolveCreateReplay(ctx, replayInput); err != nil || replayed {
		return existing, err
	}
	// RF-21 is asynchronous, so this yields one of three outcomes: publish now,
	// withhold, or refuse. Only the refusal returns here.
	links, err := s.classifyBodyLinks(ctx, workspaceID, body)
	if err != nil {
		return domain.Message{}, err
	}

	mentions, err := s.resolveOutgoingMentions(ctx, workspaceID, channelID, "", senderID, body, bodyFormat)
	if err != nil {
		return domain.Message{}, err
	}
	body = mentions.Body
	mentionedUserIDs, mentionedChannelIDs := mentions.UserIDs, mentions.ChannelIDs

	refs, err := s.validateCreateReferences(ctx, createReferenceInput{
		WorkspaceID: workspaceID, ChannelID: channelID, SenderID: senderID,
		TargetType: "channel", TargetID: channelID,
		ParentMessageID: input.ParentMessageID, ForwardedFromMessageID: input.ForwardedFromMessageID,
		ReferencedMessageID: input.ReferencedMessageID,
	})
	if err != nil {
		return domain.Message{}, err
	}
	parentID, forwardedID, referencedID := refs.ParentID, refs.ForwardedID, refs.ReferencedID

	msg, err := s.persistMessage(ctx, storage.CreateMessageInput{
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
		AttachmentIDs:          attachmentIDs,
		MaxAttachmentBytes:     s.maxMessageAttachmentBytes,
	}, links, body, replayInput, "create channel message")
	if err != nil {
		return domain.Message{}, err
	}
	return s.announceCreatedMessage(ctx, workspaceID, senderID, "channel", channelID, msg, links), nil
}

// The four steps a send goes through before it is a row, extracted because both
// create paths perform each one identically and were carrying their own copy.
//
// The split follows what the steps actually are, not an arbitrary line count:
// normalise the request, resolve the mentions in it, validate the messages it
// points at, and — after persistence — decide whether anybody is told about it.
// Authorization is deliberately *not* among them: a channel and a DM authorize
// differently, and folding two different rules into one helper is how the wrong
// one gets applied.

// createRequestInput is the raw, untrusted shape of a send.
type createRequestInput struct {
	WorkspaceID string
	TargetID    string
	// TargetField names the target in the error message, so a caller is told
	// which field they omitted rather than a generic one.
	TargetField   string
	SenderID      string
	BodyText      string
	BodyFormat    domain.MessageBodyFormat
	AttachmentIDs []string
}

// createRequest is the same send after normalisation: trimmed, bounded, and with
// every value in the form the rest of the path expects.
type createRequest struct {
	TargetID      string
	SenderID      string
	Body          string
	BodyFormat    domain.MessageBodyFormat
	AttachmentIDs []string
}

// normalizeCreateRequest applies the rules that hold for any send, before
// anything is authorized, queried or written.
//
// Everything here is decidable from the request alone — required fields,
// attachment count and shape, body length, body format — which is why it runs
// first: input that could never name a valid message costs no query and no
// provider quota.
func normalizeCreateRequest(input createRequestInput, maxAttachments int) (createRequest, error) {
	request := createRequest{
		TargetID: strings.TrimSpace(input.TargetID),
		SenderID: strings.TrimSpace(input.SenderID),
		Body:     strings.TrimSpace(input.BodyText),
	}
	if strings.TrimSpace(input.WorkspaceID) == "" || request.TargetID == "" || request.SenderID == "" {
		return createRequest{}, fmt.Errorf("%w: workspace_id, %s, and sender_id are required",
			domain.ErrInvalidInput, input.TargetField)
	}
	attachmentIDs, err := normalizeAttachmentIDs(input.AttachmentIDs, maxAttachments)
	if err != nil {
		return createRequest{}, err
	}
	if err := validateMessageContent(request.Body, len(attachmentIDs)); err != nil {
		return createRequest{}, err
	}
	bodyFormat, err := normalizeBodyFormat(input.BodyFormat)
	if err != nil {
		return createRequest{}, err
	}
	request.AttachmentIDs, request.BodyFormat = attachmentIDs, bodyFormat
	return request, nil
}

// outgoingMentions is a body with its mentions resolved: the ids the message
// records, and the text rewritten to carry the labels those ids currently have.
type outgoingMentions struct {
	Body       string
	UserIDs    []string
	ChannelIDs []string
}

// resolveOutgoingMentions authorizes the mentions in a body and rewrites their
// labels.
//
// Only v3 bodies carry mention tokens, and a body with none does no work at all
// — no query, no rewrite. The authorization is the point: a mention is resolved
// against what the *sender* may see in this target, so naming a private channel
// or a user they cannot reach is refused here rather than leaking a label.
func (s *MessageService) resolveOutgoingMentions(
	ctx context.Context, workspaceID, channelID, dmConversationID, senderID, body string,
	bodyFormat domain.MessageBodyFormat,
) (outgoingMentions, error) {
	mentions := outgoingMentions{Body: body}
	if bodyFormat != domain.MessageBodyFormatV3 {
		return mentions, nil
	}
	if dmConversationID != "" && hasMentionKind(body, "all") {
		return outgoingMentions{}, fmt.Errorf("%w: invalid mention", domain.ErrInvalidInput)
	}
	mentions.UserIDs, mentions.ChannelIDs = extractMentionIDs(body)
	if len(mentions.UserIDs)+len(mentions.ChannelIDs) == 0 {
		return mentions, nil
	}
	labels, err := s.messages.ResolveAuthorizedMentionLabels(
		ctx, workspaceID, channelID, dmConversationID, senderID, mentions.UserIDs, mentions.ChannelIDs,
	)
	if err != nil {
		return outgoingMentions{}, fmt.Errorf("resolve authorized mention labels: %w", err)
	}
	if err := validateMentionRefs(mentions.UserIDs, mentions.ChannelIDs, labels); err != nil {
		return outgoingMentions{}, err
	}
	mentions.Body = rewriteMentionLabels(mentions.Body, labels)
	return mentions, nil
}

// createReferenceInput names the three messages a send may point at, and the
// target it is being posted to.
type createReferenceInput struct {
	WorkspaceID      string
	ChannelID        string
	DMConversationID string
	SenderID         string
	// TargetType and TargetID describe where this message is going, and exist
	// only to refuse a reference that points back at it.
	TargetType             string
	TargetID               string
	ParentMessageID        string
	ForwardedFromMessageID string
	ReferencedMessageID    string
}

// createReferences is the validated result: ids that exist, belong to this
// workspace, and are readable by the sender. Empty means "none given".
type createReferences struct {
	ParentID     string
	ForwardedID  string
	ReferencedID string
}

// validateCreateReferences checks every message a send points at.
//
// Each is validated against the sender's current access, so a reference cannot
// be used to pull content out of somewhere they cannot read — and every failure
// is the same non-enumerating error, so a caller cannot tell "does not exist"
// from "exists but is not yours".
//
// The last rule is the one that needs the target: a cross-target reference to
// the conversation the message is already being posted to is not a reference,
// it is a self-link, and RF-09 refuses it.
func (s *MessageService) validateCreateReferences(
	ctx context.Context, input createReferenceInput,
) (createReferences, error) {
	parentID, err := s.validateRefMessage(ctx, input.WorkspaceID, input.ChannelID,
		input.DMConversationID, input.SenderID, strings.TrimSpace(input.ParentMessageID))
	if err != nil {
		return createReferences{}, err
	}
	forwardedID, err := s.validateRefMessage(ctx, input.WorkspaceID, input.ChannelID,
		input.DMConversationID, input.SenderID, strings.TrimSpace(input.ForwardedFromMessageID))
	if err != nil {
		return createReferences{}, err
	}
	referencedID, reference, err := s.validateReferencedMessage(ctx, input.WorkspaceID,
		input.SenderID, strings.TrimSpace(input.ReferencedMessageID))
	if err != nil {
		return createReferences{}, err
	}
	if reference != nil && reference.TargetType == input.TargetType && reference.TargetID == input.TargetID {
		return createReferences{}, domain.ErrInvalidMessageReference
	}
	return createReferences{ParentID: parentID, ForwardedID: forwardedID, ReferencedID: referencedID}, nil
}

// announceCreatedMessage completes a persisted message and decides who learns
// about it.
//
// Two things, and they belong together because the second depends on the first
// being finished. The reference preview is resolved *again*, after the write,
// because the one obtained during validation describes a moment that has passed:
// authorization and source state may both have changed while the row was being
// persisted, and the response must never serialize the older answer.
//
// Then the RF-21 decision, in one place rather than at the end of each create:
// a withheld message is announced to nobody. Nothing is broadcast, nothing is
// notified and no unread count moves, because the row is pending_link_scan and
// every read path already excludes it. The worker publishes it if the scan
// clears, and blocks it if it does not.
func (s *MessageService) announceCreatedMessage(
	ctx context.Context, workspaceID, senderID, targetType, targetID string,
	msg domain.Message, links linkDecision,
) domain.Message {
	created := []domain.Message{msg}
	if err := s.resolveMessageReferences(ctx, workspaceID, senderID, created); err != nil {
		msg.Reference = &domain.MessageReference{Available: false}
	} else {
		msg = created[0]
	}
	if links.pending() {
		return msg
	}
	s.publishMessageCreated(ctx, workspaceID, targetType, targetID, msg)
	return msg
}

// createIdentity is everything about a send that makes it *this* send.
//
// The previous round compared only the body, which meant a key reused with the
// same text but a different attachment, format, parent or reference replayed the
// original — the caller believed their new message had been created, and it had
// not. Every field the create statement persists is here; a field that changes
// what gets written must be added, and the version tag below is how that change
// is made visible rather than silent.
type createIdentity struct {
	DestinationType     string
	DestinationID       string
	BodyText            string
	BodyFormat          string
	ParentMessageID     string
	ForwardedFromID     string
	ReferencedMessageID string
	AttachmentIDs       []string
}

// createIdentityVersion tags the fingerprint's construction, so adding a field
// invalidates old values rather than letting a request that differs in the new
// field compare equal to one recorded before it existed.
const createIdentityVersion = "create.v1"
const orderedAttachmentIdentityVersion = "create.v2"

// fingerprint serialises the identity deterministically.
//
// Length-prefixed rather than delimited, so no combination of fields can be
// confused with a different one by concatenation — ("ab","c") and ("a","bc")
// must not hash alike. Zero and one attachment retain create.v1 compatibility;
// a multi-attachment message uses create.v2 and preserves order because that
// order is rendered to every recipient. Everything else is taken in a fixed order.
func (i createIdentity) fingerprint() string {
	attachments := i.AttachmentIDs
	version := createIdentityVersion
	if len(attachments) > 1 {
		version = orderedAttachmentIdentityVersion
	}

	digest := sha256.New()
	for _, field := range []string{
		version,
		i.DestinationType, i.DestinationID,
		i.BodyText, i.BodyFormat,
		i.ParentMessageID, i.ForwardedFromID, i.ReferencedMessageID,
	} {
		writeFingerprintField(digest, field)
	}
	for _, attachment := range attachments {
		writeFingerprintField(digest, attachment)
	}
	return hex.EncodeToString(digest.Sum(nil))
}

// persistMessage writes the message and resolves a concurrent replay.
//
// It carries the three things both create paths must get right and were
// repeating verbatim: the link-safety decision (initial status, the URLs to wait
// on, and the fingerprint binding the eventual promotion to this content), the
// idempotency key and its operation identity, and the read-back when two
// identical sends race and this one loses.
//
// The caller keeps everything that genuinely differs between a channel and a DM
// — authorization, references, mentions — and hands over only the part that is
// the same.
func (s *MessageService) persistMessage(
	ctx context.Context, input storage.CreateMessageInput, links linkDecision,
	body string, replayInput storage.CreateReplayInput, operation string,
) (domain.Message, error) {
	input.Status = links.messageStatus()
	input.LinkSafetyState = links.initialState()
	input.LinkScanURLs = links.URLs
	input.LinkSafetyFingerprint = links.fingerprint(body)
	input.IdempotencyKey = replayInput.IdempotencyKey
	input.RequestFingerprint = replayInput.RequestFingerprint

	msg, err := s.messages.CreateMessage(ctx, input)
	switch {
	case errors.Is(err, storage.ErrCreateReplay):
		return s.readBackRaceWinner(ctx, replayInput)
	case err != nil:
		return domain.Message{}, fmt.Errorf("%s: %w", operation, err)
	}
	return msg, nil
}

// channelReplayInput describes a channel send as an idempotent operation.
//
// The key alone is not the identity: the destination, the sender and every field
// that changes what gets written are, so a key reused for a different send is a
// conflict rather than a replay of something the caller did not ask for.
func channelReplayInput(
	workspaceID, channelID, senderID, body string,
	bodyFormat domain.MessageBodyFormat, attachmentIDs []string,
	input CreateChannelMessageInput,
) storage.CreateReplayInput {
	return storage.CreateReplayInput{
		WorkspaceID: workspaceID, ChannelID: channelID, SenderID: senderID,
		IdempotencyKey: strings.TrimSpace(input.IdempotencyKey),
		RequestFingerprint: createIdentity{
			DestinationType: "channel", DestinationID: channelID,
			BodyText: body, BodyFormat: string(bodyFormat),
			ParentMessageID:     strings.TrimSpace(input.ParentMessageID),
			ForwardedFromID:     strings.TrimSpace(input.ForwardedFromMessageID),
			ReferencedMessageID: strings.TrimSpace(input.ReferencedMessageID),
			AttachmentIDs:       attachmentIDs,
		}.fingerprint(),
	}
}

// dmReplayInput is channelReplayInput for a DM. Kept separate rather than
// generalised: the two carry different destination fields and different input
// types, and collapsing them would mean a shape that is neither.
func dmReplayInput(
	workspaceID, conversationID, senderID, body string,
	bodyFormat domain.MessageBodyFormat, attachmentIDs []string,
	input CreateDMMessageInput,
) storage.CreateReplayInput {
	return storage.CreateReplayInput{
		WorkspaceID: workspaceID, DMConversationID: conversationID, SenderID: senderID,
		IdempotencyKey: strings.TrimSpace(input.IdempotencyKey),
		RequestFingerprint: createIdentity{
			DestinationType: "dm", DestinationID: conversationID,
			BodyText: body, BodyFormat: string(bodyFormat),
			ParentMessageID:     strings.TrimSpace(input.ParentMessageID),
			ForwardedFromID:     strings.TrimSpace(input.ForwardedFromMessageID),
			ReferencedMessageID: strings.TrimSpace(input.ReferencedMessageID),
			AttachmentIDs:       attachmentIDs,
		}.fingerprint(),
	}
}

// resolveCreateReplay answers a retried send from what is already persisted.
//
// It runs before the link classification for the same reason the forward's does:
// a replay creates nothing, so it must not queue a scan, spend provider quota or
// depend on the provider agreeing a second time. A message withheld for a scan
// is exactly the case a client is most likely to retry — it sees no delivery —
// so this is what stops a flaky connection from producing five withheld copies
// and five scans.
//
// The body is compared here rather than in SQL: only this layer knows what the
// caller was about to write, and a key reused for different content is a
// conflict rather than a replay.
func (s *MessageService) resolveCreateReplay(
	ctx context.Context, input storage.CreateReplayInput,
) (domain.Message, bool, error) {
	if input.IdempotencyKey == "" {
		return domain.Message{}, false, nil
	}
	existing, err := s.messages.LookupCreateReplay(ctx, input)
	switch {
	case errors.Is(err, domain.ErrNotFound):
		return domain.Message{}, false, nil
	case err != nil:
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return domain.Message{}, false, err
		}
		return domain.Message{}, false, fmt.Errorf("lookup create replay: %w", err)
	case existing.CreateFingerprint != input.RequestFingerprint:
		// Same key, different operation. Reporting a conflict rather than
		// silently returning the old message is what keeps the key honest: a
		// client that reused it by mistake learns so instead of losing a send.
		//
		// The comparison is over the whole operation, not just the body — that
		// was the finding: an attachment, a format or a parent could differ and
		// still replay.
		return domain.Message{}, false, domain.ErrConflict
	}
	return existing, true, nil
}

// readBackRaceWinner resolves two identical sends that raced.
//
// The unique index is the authority: exactly one message exists, and the loser's
// INSERT is the one that collided. Reading back what the winner wrote is what
// turns that collision into the answer the client asked for, rather than a
// failure it cannot act on and would only retry into the same race.
//
// A collision with nothing to read back is a genuine conflict — the key belongs
// to a different message — and is reported as one.
func (s *MessageService) readBackRaceWinner(
	ctx context.Context, replayInput storage.CreateReplayInput,
) (domain.Message, error) {
	existing, replayed, err := s.resolveCreateReplay(ctx, replayInput)
	switch {
	case err != nil:
		return domain.Message{}, err
	case replayed:
		return existing, nil
	}
	return domain.Message{}, domain.ErrConflict
}

// passThrough decides which failures reach the caller unchanged.
//
// The distinction it preserves: a domain error is an *answer* — not found, not
// allowed, conflicting — and the HTTP layer maps it to a status directly, so
// prefixing it with an internal operation name only adds noise to what the
// client is told. Anything else is an internal failure, and the operation that
// produced it is the most useful thing to record about it.
//
// The sentinel list is per call site rather than a blanket rule, because which
// answers a step can legitimately produce is part of what that step means. It
// was written out three times in the forward path, identically enough to be
// worth stating once and differently enough to be worth passing in.
func passThrough(err error, operation string, unchanged ...error) error {
	for _, sentinel := range unchanged {
		if errors.Is(err, sentinel) {
			return err
		}
	}
	return fmt.Errorf("%s: %w", operation, err)
}

// ForwardChannelMessage creates a server-side snapshot of an authorized source
// message in another authorized channel. Source provenance never leaves the API.
func (s *MessageService) ForwardChannelMessage(ctx context.Context, input ForwardChannelMessageInput) (ForwardChannelMessageOutput, error) {
	workspaceID := strings.TrimSpace(input.WorkspaceID)
	destinationChannelID := strings.TrimSpace(input.DestinationChannelID)
	actorID := strings.TrimSpace(input.ActorID)
	sourceMessageID := strings.TrimSpace(input.SourceMessageID)
	if workspaceID == "" || destinationChannelID == "" || actorID == "" || sourceMessageID == "" {
		return ForwardChannelMessageOutput{}, fmt.Errorf("%w: forwarding identifiers are required", domain.ErrInvalidInput)
	}

	// Idempotency first, and before anything leaves this process.
	//
	// A retried forward is a request for the message that already exists, not a
	// request to create one, so it must not depend on a third party agreeing
	// twice: a verdict that flipped to malicious after the original send, or a
	// provider that is simply down, would otherwise turn a legitimate retry of
	// an already-persisted message into a refusal. Nothing new is published
	// here, so nothing new needs checking.
	//
	// Two concurrent *first* attempts can both miss this and both check. That is
	// allowed: the unique index below is still the only thing that decides which
	// one inserts, one message is created, and the other is told it replayed. A
	// duplicate provider lookup in a rare race is not worth a distributed lock.
	idempotencyKey := strings.TrimSpace(input.IdempotencyKey)
	if idempotencyKey != "" {
		replay, err := s.messages.LookupForwardReplay(ctx, storage.ForwardReplayInput{
			WorkspaceID: workspaceID, DestinationChannelID: destinationChannelID,
			ActorID: actorID, SourceMessageID: sourceMessageID,
			IdempotencyKey: idempotencyKey,
		})
		switch {
		case err == nil:
			return ForwardChannelMessageOutput{Message: replay, Replayed: true}, nil
		case errors.Is(err, domain.ErrNotFound):
			// No earlier forward under this key: carry on and create one.
		default:
			return ForwardChannelMessageOutput{}, passThrough(err, "lookup forward replay",
				domain.ErrConflict, context.Canceled, context.DeadlineExceeded)
		}
	}

	// RF-21. A forward creates a *new* message, so it is a way to publish content
	// that was written before the check existed — or while it was switched off —
	// into a channel where it never passed one. It goes through the same gate.
	//
	// The order is what makes it correct rather than decorative. The snapshot is
	// read first, outside any transaction and holding no row lock, so the
	// provider call below never happens with a database connection pinned; the
	// snapshot is then what is checked *and* what the statement writes, so a
	// concurrent edit of the source cannot swap the content between the two. The
	// source-side authorization the snapshot query applies is the same one the
	// forwarding statement applies, and the destination-side authorization is
	// untouched — it still lives in the atomic statement below.
	snapshot, err := s.messages.SnapshotForwardableMessage(ctx, storage.ForwardSnapshotInput{
		WorkspaceID: workspaceID, DestinationChannelID: destinationChannelID,
		ActorID: actorID, SourceMessageID: sourceMessageID,
	})
	if err != nil {
		return ForwardChannelMessageOutput{}, passThrough(err, "snapshot forwardable message",
			domain.ErrNotFound, context.Canceled, context.DeadlineExceeded)
	}
	links, err := s.classifyBodyLinks(ctx, workspaceID, snapshot.BodyText)
	if err != nil {
		return ForwardChannelMessageOutput{}, err
	}

	result, err := s.messages.ForwardChannelMessage(ctx, storage.ForwardChannelMessageInput{
		WorkspaceID: workspaceID, DestinationChannelID: destinationChannelID,
		ActorID: actorID, SourceMessageID: sourceMessageID,
		IdempotencyKey:        idempotencyKey,
		BodyText:              snapshot.BodyText,
		BodyFormat:            snapshot.BodyFormat,
		Status:                links.messageStatus(),
		LinkSafetyState:       links.initialState(),
		LinkScanURLs:          links.URLs,
		LinkSafetyFingerprint: links.fingerprint(snapshot.BodyText),
	})
	if err != nil {
		return ForwardChannelMessageOutput{}, passThrough(err, "forward channel message",
			domain.ErrInvalidInput, domain.ErrNotFound, domain.ErrConflict,
			context.Canceled, context.DeadlineExceeded)
	}
	if !result.Replayed && !links.pending() {
		s.publishMessageCreated(ctx, workspaceID, "channel", destinationChannelID, result.Message)
	}
	return ForwardChannelMessageOutput{
		Message: result.Message, Replayed: result.Replayed, Pending: links.pending(),
	}, nil
}

// CreateDMMessage posts a message to a DM conversation.
// The sender must be an active workspace member and an active DM participant.
// Archived DM conversations are denied. Cross-workspace targets are denied.
// The caller cannot set status, timestamps, edited_at, or deleted_at.
func (s *MessageService) CreateDMMessage(ctx context.Context, input CreateDMMessageInput) (domain.Message, error) {
	workspaceID := strings.TrimSpace(input.WorkspaceID)
	request, err := normalizeCreateRequest(createRequestInput{
		WorkspaceID: workspaceID, TargetID: input.ConversationID, TargetField: "conversation_id",
		SenderID: input.SenderID, BodyText: input.BodyText,
		BodyFormat: input.BodyFormat, AttachmentIDs: input.AttachmentIDs,
	}, s.maxMessageAttachments)
	if err != nil {
		return domain.Message{}, err
	}
	conversationID, senderID := request.TargetID, request.SenderID
	body, bodyFormat, attachmentIDs := request.Body, request.BodyFormat, request.AttachmentIDs

	// SQL-enforce DM visibility: workspace active + workspace member active +
	// DM conversation active + active DM membership. Returns ErrNotFound for all
	// invisible targets (non-enumerating).
	conversation, err := s.dms.GetVisibleConversationByID(ctx, workspaceID, conversationID, senderID)
	if err != nil {
		return domain.Message{}, err
	}
	// RF-21, on the same terms as the channel path: after authorization, before
	// persistence. A DM is the likelier phishing vector of the two, not the
	// lesser one.
	replayInput := dmReplayInput(workspaceID, conversationID, senderID, body, bodyFormat, attachmentIDs, input)
	if existing, replayed, err := s.resolveCreateReplay(ctx, replayInput); err != nil || replayed {
		return existing, err
	}

	links, err := s.classifyBodyLinks(ctx, workspaceID, body)
	if err != nil {
		return domain.Message{}, err
	}

	mentions, err := s.resolveOutgoingMentions(ctx, workspaceID, "", conversationID, senderID, body, bodyFormat)
	if err != nil {
		return domain.Message{}, err
	}
	// Direct DMs keep their existing codec and cannot gain mention semantics by
	// manually posting a v3 token. Group membership is the only DM authority.
	if conversation.Type != domain.DMConversationTypeGroup && len(mentions.UserIDs)+len(mentions.ChannelIDs) > 0 {
		return domain.Message{}, fmt.Errorf("%w: invalid mention", domain.ErrInvalidInput)
	}
	body = mentions.Body

	refs, err := s.validateCreateReferences(ctx, createReferenceInput{
		WorkspaceID: workspaceID, DMConversationID: conversationID, SenderID: senderID,
		TargetType: "dm", TargetID: conversationID,
		ParentMessageID: input.ParentMessageID, ForwardedFromMessageID: input.ForwardedFromMessageID,
		ReferencedMessageID: input.ReferencedMessageID,
	})
	if err != nil {
		return domain.Message{}, err
	}
	parentID, forwardedID, referencedID := refs.ParentID, refs.ForwardedID, refs.ReferencedID

	msg, err := s.persistMessage(ctx, storage.CreateMessageInput{
		WorkspaceID:            workspaceID,
		DMConversationID:       conversationID,
		SenderID:               senderID,
		Kind:                   domain.MessageKindUser,
		BodyText:               body,
		BodyFormat:             bodyFormat,
		ParentMessageID:        parentID,
		ForwardedFromMessageID: forwardedID,
		ReferencedMessageID:    referencedID,
		MentionedUserIDs:       mentions.UserIDs,
		MentionedChannelIDs:    mentions.ChannelIDs,
		AttachmentIDs:          attachmentIDs,
		MaxAttachmentBytes:     s.maxMessageAttachmentBytes,
	}, links, body, replayInput, "create dm message")
	if err != nil {
		return domain.Message{}, err
	}
	return s.announceCreatedMessage(ctx, workspaceID, senderID, "dm", conversationID, msg, links), nil
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
	// Eligibility before cost, which is the order the previous round had wrong.
	//
	// Classifying a body queues Cloudflare scans, and it used to happen before
	// anything had established that this edit could ever be applied — so an
	// authenticated user could spend the account's scan quota editing a message
	// they do not own, or one that is already deleted, and only find out
	// afterwards. Refusing here costs one comparison against a row that was
	// already read.
	//
	// The window is deliberately not checked here: it lives on the workspace and
	// reading it would cost the query this check exists to avoid. It is enforced
	// where it has always been enforced — inside EditMessage's transaction, under
	// FOR UPDATE, against the database's own clock — so an edit that squeaks past
	// this and expires in between is still refused. This is an optimisation in
	// front of the authority, never a replacement for it.
	if err := domain.ValidateMessageEdit(current, editorID, nil, time.Now()); err != nil {
		return domain.Message{}, err
	}
	// RF-21 applies to editing too, and not as an afterthought: a check that ran
	// only on creation would be bypassed by sending a clean message and editing
	// the link in. This is the same funnel — one body, one rule.
	//
	// Editing is the one path that cannot go pending. A withheld *edit* would
	// mean either showing everyone the unscanned new body or silently keeping
	// the old one while telling the author it was saved, and both are worse than
	// asking the author to retry: the currently published version stays exactly
	// as it is, and the scan the classification just queued makes the retry
	// succeed shortly. A pending revision table is the upgrade if authors ever
	// find the retry intrusive.
	editLinks, err := s.classifyBodyLinks(ctx, workspaceID, body)
	if err != nil {
		return domain.Message{}, err
	}
	// An edit publishes immediately or not at all, so the two halves of "pending"
	// part company here — the one place they do.
	//
	// A URL nothing has decided means waiting: there is no answer and this path
	// will not produce one. A URL whose scan is terminal-without-verdict is
	// decided, and the edit lands carrying the same `inconclusive` marker a created
	// message ends up with, with the same consequences — the reader may click, this
	// server may not fetch. Refusing that case would make a message permanently
	// uneditable whenever one of its links happened to be one Cloudflare declined
	// to scan.
	editState, editable := editLinks.editState()
	if !editable {
		return domain.Message{}, domain.ErrURLCheckPending
	}
	body, err = s.resolveAndRewriteMentions(ctx, workspaceID, current.ChannelID, current.DMConversationID, editorID, body, bodyFormat)
	if err != nil {
		return domain.Message{}, err
	}

	// The fingerprint is computed over the *rewritten* body, which is the text the
	// URLs were extracted from after mention resolution — the same binding
	// creation uses, so a verdict obtained for one content can never decide
	// another.
	updated, err := s.messages.EditMessage(ctx, storage.EditMessageInput{
		WorkspaceID: workspaceID, MessageID: messageID, EditorID: editorID,
		Body: body, BodyFormat: bodyFormat,
		LinkSafetyState:       editState,
		LinkSafetyFingerprint: editLinks.fingerprint(body),
		LinkScanURLs:          editLinks.URLs,
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

func (s *MessageService) resolveAndRewriteMentions(ctx context.Context, workspaceID, channelID, dmConversationID, requesterID, body string, bodyFormat domain.MessageBodyFormat) (string, error) {
	if bodyFormat != domain.MessageBodyFormatV3 {
		return body, nil
	}
	if dmConversationID != "" && hasMentionKind(body, "all") {
		return "", fmt.Errorf("%w: invalid mention", domain.ErrInvalidInput)
	}
	userIDs, channelIDs := extractMentionIDs(body)
	if len(userIDs)+len(channelIDs) == 0 {
		return body, nil
	}
	labels, err := s.messages.ResolveAuthorizedMentionLabels(
		ctx, workspaceID, channelID, dmConversationID, requesterID, userIDs, channelIDs,
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
	messageIDs, err := normalizeMessageIDBatch(input.MessageIDs, MaxMessageReferenceBatchSize)
	if err != nil {
		return nil, err
	}

	if err := s.authorizeTarget(ctx, workspaceID, channelID, dmConversationID, callerID); err != nil {
		return nil, err
	}

	// Two reads, and the split is the authorization. The first says which of the
	// caller's destination messages carry a reference at all; the second
	// re-authorizes each *origin* separately, because being able to read a reply
	// says nothing about being able to read what it replies to.
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
	return assembleReferenceResolutions(messageIDs, referencedIDs, resolved), nil
}

// authorizeTarget re-checks that the caller may currently read the conversation
// they are asking about, whichever kind it is.
//
// Exactly one of channelID and dmConversationID is set; the callers that reach
// here have already established that. Both paths answer ErrNotFound for
// everything invisible, so neither can be used to discover that a target exists.
func (s *MessageService) authorizeTarget(
	ctx context.Context, workspaceID, channelID, dmConversationID, callerID string,
) error {
	if channelID != "" {
		_, err := s.channels.GetVisibleChannelByID(ctx, workspaceID, channelID, callerID)
		return err
	}
	_, err := s.dms.GetVisibleConversationByID(ctx, workspaceID, dmConversationID, callerID)
	return err
}

// assembleReferenceResolutions pairs every requested message with the preview
// its origin resolved to.
//
// Every requested id gets an entry, and the default is Available: false. That is
// the non-enumerating part of the contract: a destination that carries no
// reference, one whose origin was deleted, and one whose origin the caller may
// not read are all reported identically, so the response says nothing about
// which of the three happened.
func assembleReferenceResolutions(
	messageIDs []string, referencedIDs map[string]string,
	resolved map[string]domain.MessageReference,
) []MessageReferenceResolution {
	result := make([]MessageReferenceResolution, 0, len(messageIDs))
	for _, destinationID := range messageIDs {
		reference := domain.MessageReference{Available: false}
		// The empty check is not redundant with the lookup: a destination with no
		// reference maps to "", and resolving "" against the map would hand every
		// unreferenced message whatever happened to be stored under that key.
		if sourceID := referencedIDs[destinationID]; sourceID != "" {
			if available, ok := resolved[sourceID]; ok {
				reference = available
			}
		}
		result = append(result, MessageReferenceResolution{MessageID: destinationID, Reference: reference})
	}
	return result
}

// validateMessageBody is the rule for every path where a body is the whole
// message: editing, in particular, which may not empty a message out.
func validateMessageBody(body string) error {
	return validateMessageContent(body, 0)
}

// validateMessageContent is the creation rule: a message needs something in it,
// and an attachment is something (RF-32).
//
// Empty body plus no attachment stays invalid, which is what keeps the empty
// send that has always been refused refused. Body plus attachment, and
// attachment alone, are both valid. The length ceiling is unchanged and applies
// regardless of attachments.
func validateMessageContent(body string, attachmentCount int) error {
	if body == "" && attachmentCount == 0 {
		return fmt.Errorf("%w: body_text or attachment_ids is required", domain.ErrInvalidInput)
	}
	if len([]rune(body)) > maxMessageBodyRunes {
		return fmt.Errorf("%w: body_text exceeds maximum length of %d characters", domain.ErrInvalidInput, maxMessageBodyRunes)
	}
	return nil
}

// normalizeAttachmentIDs turns the client's list into the canonical, bounded,
// duplicate-free one the storage layer may re-validate (RF-32).
//
// Nothing here decides whether an attachment may be linked — that is a database
// question and is answered atomically with the INSERT. This only refuses input
// that could never name a valid attachment, and does it before any query runs:
// too many ids, an empty or malformed one, or the same id twice. A duplicate is
// an error rather than something to silently collapse, because a client sending
// one is not describing the message it thinks it is.
func normalizeAttachmentIDs(rawIDs []string, maxAttachments int) ([]string, error) {
	if len(rawIDs) == 0 {
		return nil, nil
	}
	if maxAttachments < 1 || maxAttachments > domain.MaxMessageAttachments {
		maxAttachments = domain.MaxMessageAttachments
	}
	if len(rawIDs) > maxAttachments {
		return nil, fmt.Errorf("%w: at most %d attachment_ids are allowed",
			domain.ErrInvalidInput, maxAttachments)
	}
	ids := make([]string, 0, len(rawIDs))
	seen := make(map[string]struct{}, len(rawIDs))
	for _, rawID := range rawIDs {
		parsed, err := uuid.Parse(strings.TrimSpace(rawID))
		if err != nil || parsed == uuid.Nil {
			return nil, fmt.Errorf("%w: invalid attachment id", domain.ErrInvalidInput)
		}
		id := parsed.String()
		if _, exists := seen[id]; exists {
			return nil, fmt.Errorf("%w: duplicate attachment id", domain.ErrInvalidInput)
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	return ids, nil
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
