package storage

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/nicrepository/nchat/libs/go/platform/urlsafety"
	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
)

// defaultMessageLimit is the number of messages returned when no limit is specified.
const defaultMessageLimit = 50

// maxMessageLimit is the maximum number of messages that may be requested per page.
const maxMessageLimit = 100

// MessageCursor identifies the stable position of a message in the time-ordered list.
// It encodes (created_at, id) to allow keyset pagination without offset drift.
type MessageCursor struct {
	CreatedAt time.Time
	ID        string
}

// EncodeCursor serializes a MessageCursor to an opaque URL-safe base64 string.
// Format (plaintext before encoding): RFC3339Nano "|" UUID.
func EncodeCursor(c MessageCursor) string {
	raw := c.CreatedAt.UTC().Format(time.RFC3339Nano) + "|" + c.ID
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

// DecodeCursor parses an opaque cursor string produced by EncodeCursor.
// Returns domain.ErrInvalidCursor for any malformed or invalid input.
func DecodeCursor(s string) (MessageCursor, error) {
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return MessageCursor{}, domain.ErrInvalidCursor
	}
	parts := strings.SplitN(string(b), "|", 2)
	if len(parts) != 2 {
		return MessageCursor{}, domain.ErrInvalidCursor
	}
	ts, err := time.Parse(time.RFC3339Nano, parts[0])
	if err != nil {
		return MessageCursor{}, domain.ErrInvalidCursor
	}
	if _, err := uuid.Parse(parts[1]); err != nil {
		return MessageCursor{}, domain.ErrInvalidCursor
	}
	return MessageCursor{CreatedAt: ts.UTC(), ID: parts[1]}, nil
}

// ListMessagesResult is the result of a paginated message list query.
// Messages are sorted oldest-first (ASC by created_at, id).
// NextCursor, when non-nil, is the cursor to pass as BeforeCursor in a subsequent
// request to retrieve the next (older) page.
type ListMessagesResult struct {
	Messages   []domain.Message
	NextCursor *MessageCursor
}

// CreateMessageInput holds caller-validated fields for inserting a message.
// Exactly one of ChannelID and DMConversationID must be non-empty.
// ParentMessageID, ForwardedFromMessageID, and ReferencedMessageID are optional
// (empty string = NULL). Kind defaults to 'user' when empty.
type CreateMessageInput struct {
	WorkspaceID            string
	ChannelID              string
	DMConversationID       string
	SenderID               string
	Kind                   domain.MessageKind
	BodyText               string
	BodyFormat             domain.MessageBodyFormat
	ParentMessageID        string
	ForwardedFromMessageID string
	ReferencedMessageID    string
	MentionedUserIDs       []string
	MentionedChannelIDs    []string
	// MentionAllGroupMembers is issue #776's @all in a group DM. It is never
	// set for a channel — channel @all is unchanged, still purely textual —
	// and the service sets it only after re-deriving that the DM target really
	// is an active group and the body really carries an "all" token.
	//
	// It is deliberately not a recipient list: trusting an id list the service
	// computed ahead of the write is exactly the TOCTOU a membership change
	// between fetch and send would exploit. Instead this is a flag CreateMessage
	// resolves itself, in the same statement as the insert, by reading
	// chat.dm_members directly under that statement's own read-committed
	// snapshot — so a member removed (or added) by a change that *committed*
	// before this statement began is correctly reflected either way. That is
	// not the same claim as serializing against a change strictly concurrent
	// with this statement's own execution: this CTE takes no row lock on
	// dm_members/workspace_members, so a removal committing mid-statement is
	// ordinary read-committed visibility, not a guarantee this design makes or
	// needs — #776's requirement is "the source of truth at the authoritative
	// moment wins" against a membership change that already happened, which a
	// pre-computed list would miss entirely; it is not a request for
	// serializable isolation against a write racing the send itself.
	MentionAllGroupMembers bool
	// AttachmentIDs are candidate files.attachments ids, already parsed as
	// canonical UUIDs, deduplicated and bounded by the service. They are
	// candidates only: CreateMessage re-validates every one of them against the
	// database in the same statement that inserts the message, and a message is
	// not created at all when any of them fails.
	AttachmentIDs []string
	// MaxAttachmentBytes is the server-owned aggregate size ceiling applied in
	// the same statement that locks and associates all candidate attachments.
	MaxAttachmentBytes int64

	// Status is the lifecycle state the message is created in. Empty means
	// active, which is what every path that carries no link uses.
	//
	// RF-21 is the only reason it is settable: a message whose links have not
	// been scanned is created as MessageStatusPendingLinkScan, which every read
	// path already excludes, so it is withheld from everyone until the worker
	// promotes it. The service decides this; a client cannot ask for a status.
	Status domain.MessageStatus
	// LinkSafetyState is derived by the service from the same verdict snapshot as
	// Status. Empty is correct for linkless and withheld messages; a cached-safe
	// active message must carry safe immediately.
	LinkSafetyState domain.MessageLinkSafety

	// RequestFingerprint is the identity of the operation this key stands for.
	// Persisted beside the key so a replay can be told from a reuse.
	RequestFingerprint string

	// IdempotencyKey makes a retried send return the original message instead of
	// creating a second one. Empty means no idempotency, which is what every
	// caller that does not supply the header gets.
	//
	// It matters more since RF-21: a withheld message is invisible to everyone
	// but its author, so a client with a dropped response has every reason to
	// send again — and would otherwise get a second withheld message and a
	// second scan.
	IdempotencyKey string

	// LinkScanURLs are the canonical URLs this message is waiting on (RF-21),
	// written in the same statement that creates it. They must already exist in
	// chat.link_scans — EnsureLinkScans puts them there — because the join
	// table references it.
	LinkScanURLs []string

	// LinkSafetyFingerprint identifies the content those URLs were extracted
	// from. It is stamped on the message and on every association in the same
	// statement, so the promotion can require that the verdicts it is reading
	// belong to the content it is about to publish.
	LinkSafetyFingerprint string
}

// createIdempotencyConstraint is the unique index that makes a retried send
// collide instead of creating a second message. Named here because the error
// path has to tell that collision apart from every other unique violation.
const createIdempotencyConstraint = "messages_create_idempotency_unique"

// ErrCreateReplay reports that this idempotency key already created a message.
//
// It is not an error the caller surfaces: it means "somebody won the race, go
// and read what they wrote". CreateMessage returns it instead of a message, and
// the service answers by looking the original up.
var ErrCreateReplay = errors.New("create message: idempotency key already used")

// CreateReplayInput identifies a possible replay of an earlier send.
//
// Every field is part of the identity, which is what stops a key from replaying
// across users or destinations: presenting somebody else's key simply matches
// nothing and creates the caller's own message.
type CreateReplayInput struct {
	WorkspaceID      string
	ChannelID        string
	DMConversationID string
	SenderID         string
	IdempotencyKey   string
	// RequestFingerprint identifies the whole operation — body, format, parent,
	// references and attachments — so a key reused for a different send is a
	// conflict rather than a replay of something the caller did not ask for.
	RequestFingerprint string
}

// LookupCreateReplay returns the message an earlier send with this key created.
//
// It is the same shape as LookupForwardReplay, deliberately: that pattern was
// reviewed and approved in the previous round, and a second idempotency
// mechanism with different semantics is how two paths start disagreeing.
//
// Returns ErrNotFound when this key created nothing the caller may see. The
// caller compares the body itself — a key reused for different content is a
// conflict, not a replay — because only the caller knows what it was about to
// write.
func (s *PGXMessageStore) LookupCreateReplay(
	ctx context.Context, input CreateReplayInput,
) (domain.Message, error) {
	if strings.TrimSpace(input.IdempotencyKey) == "" {
		return domain.Message{}, domain.ErrNotFound
	}
	var storedFingerprint string
	row := s.pool.QueryRow(ctx, `
		SELECT `+listMessageWithQuoteColumns("m", "$2", "q")+`,
		       COALESCE(m.create_request_fingerprint, '')
		FROM chat.messages m
		LEFT JOIN auth.users u ON u.id = m.sender_id`+quotedMessageJoin("m", "q")+`
		WHERE m.workspace_id = $1::uuid
		  AND m.sender_id = $2::uuid
		  AND COALESCE(m.channel_id, m.dm_conversation_id) = $3::uuid
		  AND m.create_idempotency_key = $4`,
		input.WorkspaceID, input.SenderID,
		nullableUUID(replayTarget(input)), input.IdempotencyKey,
	)
	message, err := scanMessageWithSenderAndQuoteExtra(row, &storedFingerprint)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.Message{}, domain.ErrNotFound
		}
		return domain.Message{}, fmt.Errorf("lookup create replay: %w", err)
	}
	message.CreateFingerprint = storedFingerprint
	return message, nil
}

// replayTarget is the destination half of the key's identity: a channel or a DM
// conversation, whichever this send names.
func replayTarget(input CreateReplayInput) string {
	if input.ChannelID != "" {
		return input.ChannelID
	}
	return input.DMConversationID
}

// messageStatusOrActive resolves the default. It exists so "no status supplied"
// can only ever mean the state every message had before RF-21, and never the
// zero value of a string reaching a CHECK constraint.
func messageStatusOrActive(status domain.MessageStatus) domain.MessageStatus {
	if status == "" {
		return domain.MessageStatusActive
	}
	return status
}

// ForwardSnapshotInput identifies the source message a forward would copy. Every
// field is server-derived; the client supplies only the source message ID, which
// this query re-authorizes rather than trusts.
type ForwardSnapshotInput struct {
	WorkspaceID          string
	DestinationChannelID string
	ActorID              string
	SourceMessageID      string
}

// ForwardSnapshot is the content a forward will persist, read once.
//
// It exists so the content that is *checked* and the content that is *written*
// are the same bytes. Before it, the forwarding statement read source.body_text
// for itself, so anything the service had validated beforehand described a row
// the INSERT then re-read — a concurrent edit in between would have persisted
// something nobody checked (RF-21).
type ForwardSnapshot struct {
	SourceMessageID string
	BodyText        string
	BodyFormat      domain.MessageBodyFormat
}

// ForwardChannelMessageInput contains only server-derived forwarding fields.
//
// BodyText and BodyFormat are the ForwardSnapshot the caller has already read
// and validated, and the statement writes exactly them rather than re-reading
// the source. They are never client-supplied: the HTTP layer's forward payload
// carries an ID and nothing else, and the service fills these from
// SnapshotForwardableMessage.
type ForwardChannelMessageInput struct {
	WorkspaceID          string
	DestinationChannelID string
	ActorID              string
	SourceMessageID      string
	IdempotencyKey       string
	BodyText             string
	BodyFormat           domain.MessageBodyFormat

	// Status, LinkScanURLs and LinkSafetyFingerprint carry RF-21 exactly as they
	// do for CreateMessage: a forwarded snapshot whose links are not cleared is
	// written withheld, the URLs it waits on are recorded in the same statement,
	// and the fingerprint binds the eventual promotion to this exact snapshot.
	Status                domain.MessageStatus
	LinkSafetyState       domain.MessageLinkSafety
	LinkScanURLs          []string
	LinkSafetyFingerprint string
}

// ForwardChannelMessageResult distinguishes the original insert from an
// idempotent replay so callers do not publish duplicate side effects.
type ForwardChannelMessageResult struct {
	Message  domain.Message
	Replayed bool
}

// ForwardReplayInput identifies the message an earlier forward already created.
//
// It is the ON CONFLICT key of ForwardChannelMessage plus the source, so the
// same three answers the forwarding statement gives — replay, conflict, neither
// — can be obtained without writing anything.
type ForwardReplayInput struct {
	WorkspaceID          string
	DestinationChannelID string
	ActorID              string
	SourceMessageID      string
	IdempotencyKey       string
}

// EditMessageInput contains only server-derived identity and validated body fields.
type EditMessageInput struct {
	WorkspaceID string
	MessageID   string
	EditorID    string
	Body        string
	BodyFormat  domain.MessageBodyFormat
	// LinkSafetyState, LinkSafetyFingerprint and LinkScanURLs describe the link
	// safety of the *new* body, and they are applied in the same transaction as the
	// body itself (issue #135, CQ-001).
	//
	// Before they existed the edit replaced the body and left chat.message_link_scans
	// untouched, so the message went on being decided by the URLs of a version
	// nobody could read any more: editing a link out of a message left its
	// association behind, and a later condemnation of that URL blanked a message
	// that no longer contained it.
	LinkSafetyState       domain.MessageLinkSafety
	LinkSafetyFingerprint string
	LinkScanURLs          []string
}

// DeleteMessageInput contains only server-derived identity and scope fields.
type DeleteMessageInput struct {
	WorkspaceID string
	MessageID   string
	RequesterID string
}

// ListMessageEditHistoryInput identifies one authorized history page.
type ListMessageEditHistoryInput struct {
	WorkspaceID string
	MessageID   string
	UserID      string
	Limit       int
	Offset      int
}

// MessageSecuritySnapshot is the narrow, body-free projection used to repair a
// visible timeline after missed realtime link-safety events.
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

// ListChannelMessagesInput identifies the paged message list for a channel.
type ListChannelMessagesInput struct {
	WorkspaceID string
	ChannelID   string
	// UserID is the requesting caller; SQL enforces channel visibility for this user.
	UserID string
	// BeforeCursor, when non-nil, restricts results to messages older than the cursor.
	// When nil, the most recent Limit messages are returned.
	BeforeCursor *MessageCursor
	// Limit is the maximum number of messages to return. 0 uses the default (50).
	// Values above maxMessageLimit (100) are capped.
	Limit int
}

// ListDMMessagesInput identifies the paged message list for a DM conversation.
type ListDMMessagesInput struct {
	WorkspaceID    string
	ConversationID string
	// UserID is the requesting caller; SQL enforces DM visibility for this user.
	UserID string
	// BeforeCursor, when non-nil, restricts results to messages older than the cursor.
	// When nil, the most recent Limit messages are returned.
	BeforeCursor *MessageCursor
	// Limit is the maximum number of messages to return. 0 uses the default (50).
	// Values above maxMessageLimit (100) are capped.
	Limit int
}

// MessageStore is the persistence interface for message operations.
type MessageStore interface {
	// CreateMessage inserts a new message and returns the persisted record.
	// Returns ErrInvalidMessageReference when any of the optional reference fields
	// (parent, forwarded_from, referenced) do not belong to the same workspace and
	// same target as the new message. This is a non-enumerating storage backstop:
	// callers cannot determine whether the referenced message exists.
	CreateMessage(ctx context.Context, input CreateMessageInput) (domain.Message, error)
	// SnapshotForwardableMessage returns the content a forward would copy, after
	// applying the same source-side authorization the forwarding statement
	// applies. It takes no lock and opens no transaction, so the caller may run
	// a third-party safety check on the result without holding a connection.
	// Returns ErrNotFound for every inaccessible or invalid source.
	SnapshotForwardableMessage(ctx context.Context, input ForwardSnapshotInput) (ForwardSnapshot, error)
	// LookupForwardReplay resolves an idempotency key against what is already
	// persisted, without writing. It returns the earlier message, ErrNotFound
	// when this key created nothing the caller may see, or ErrConflict when the
	// key belongs to a forward of a different source — the same conflict
	// ForwardChannelMessage reports, decided by the same rule.
	LookupForwardReplay(ctx context.Context, input ForwardReplayInput) (domain.Message, error)
	// LoadLinkVerdicts returns the fresh RF-21 verdicts for canonical URLs. A
	// URL absent from the result has no usable verdict and is never safe.
	LoadLinkVerdicts(ctx context.Context, canonicalURLs []string) (map[string]urlsafety.Verdict, error)
	// LookupCreateReplay resolves a send idempotency key against what is already
	// persisted, without writing.
	LookupCreateReplay(ctx context.Context, input CreateReplayInput) (domain.Message, error)
	// AdmitLinkScans reserves capacity for the URLs that need new provider work
	// and queues them, atomically. It replaces the unconditional EnsureLinkScans:
	// creating a job is spending money at the provider, so it now has to be
	// admitted rather than simply performed.
	AdmitLinkScans(ctx context.Context, workspaceID string, canonicalURLs []string, capacity LinkScanCapacity) (LinkScanAdmission, error)
	// ClaimDueLinkScans leases URLs awaiting a verdict for one worker pass.
	ClaimDueLinkScans(ctx context.Context, batchSize int) ([]LinkScanJob, error)
	// BeginLinkScanSubmit records the intent to submit before the provider is
	// called, so a crash leaves an attempt that can be reconciled rather than one
	// indistinguishable from a URL nobody ever submitted.
	BeginLinkScanSubmit(ctx context.Context, canonicalURL string, expectedGeneration int) (int, error)
	// RecordLinkScanSubmission stores the provider's scan id for a URL, bound to
	// the attempt generation that obtained it.
	RecordLinkScanSubmission(ctx context.Context, canonicalURL, scanUUID string, generation int) error
	// AdoptScanUUID binds a scan id recovered from the provider's search to the
	// uncertain attempt that produced it.
	AdoptScanUUID(ctx context.Context, canonicalURL, scanUUID string, generation int) error
	// ReserveProviderSubmit takes one submission from the deployment-wide
	// provider allowance, shared across replicas.
	ReserveProviderSubmit(ctx context.Context, limit int, window time.Duration) (bool, error)
	// PruneLinkScanBudget drops budget windows that can no longer be counted into.
	PruneLinkScanBudget(ctx context.Context, olderThan time.Duration) error
	// RecordLinkVerdict stores a final verdict. Non-final verdicts are refused.
	RecordLinkVerdict(ctx context.Context, canonicalURL, scanUUID string, verdict urlsafety.Verdict) error
	// ReopenExpiredVerdicts requeues lapsed verdicts that withheld messages are
	// still waiting on, so a stale clearance neither promotes nor strands.
	ReopenExpiredVerdicts(ctx context.Context) (int, error)
	// LinkSafetyStates reports what became of the caller's own messages, so a
	// client that missed the realtime verdict can recover it. Scoped to
	// senderID; ids it will not answer about are omitted rather than denied.
	LinkSafetyStates(ctx context.Context, workspaceID, senderID string, messageIDs []string) ([]domain.MessageLinkSafetyState, error)
	// LinkScanBacklog reports queue depth by state and the age of the oldest
	// waiting URL, for the pipeline gauges.
	LinkScanBacklog(ctx context.Context) (map[string]int, time.Duration, error)
	// ResolveDecidedMessages promotes or blocks withheld messages whose links
	// have all been decided, writing the promotion event in the same statement.
	ResolveDecidedMessages(ctx context.Context) (ResolveSummary, error)
	// ClaimPublishEvents leases undelivered promotion events for broadcast.
	ClaimPublishEvents(ctx context.Context, batchSize int) ([]PublishEvent, error)
	// MarkPublished retires a delivered promotion event.
	MarkPublished(ctx context.Context, messageID string) error
	// PublishOutboxBacklog reports undelivered events and the oldest one's age.
	PublishOutboxBacklog(ctx context.Context) (int, time.Duration, error)
	// ForwardChannelMessage atomically re-authorizes both channels and inserts or
	// replays the forwarded destination message, writing the snapshot the caller
	// supplies rather than re-reading the source.
	ForwardChannelMessage(ctx context.Context, input ForwardChannelMessageInput) (ForwardChannelMessageResult, error)
	EditMessage(ctx context.Context, input EditMessageInput) (domain.Message, error)
	DeleteMessage(ctx context.Context, input DeleteMessageInput) (domain.Message, bool, error)
	ListMessageEditHistory(ctx context.Context, input ListMessageEditHistoryInput) ([]domain.MessageEditHistory, error)

	// GetMessageByIDInWorkspace returns the message only if it belongs to workspaceID.
	// Returns ErrNotFound when the message does not exist or belongs to a different
	// workspace, preventing cross-workspace enumeration via message IDs.
	GetMessageByIDInWorkspace(ctx context.Context, workspaceID, messageID, userID string) (domain.Message, error)
	ListMessageSecuritySnapshots(ctx context.Context, workspaceID, userID, channelID, dmConversationID string, messageIDs []string) ([]MessageSecuritySnapshot, error)

	// ValidateRefMessageInTarget checks that messageID belongs to the given workspace
	// and target (channelID or dmConversationID). Returns nil when valid.
	// Returns ErrInvalidMessageReference for any invalid case — non-enumerating:
	// missing, cross-workspace, cross-channel, and cross-DM all return the same error.
	ValidateRefMessageInTarget(ctx context.Context, workspaceID, channelID, dmConversationID, messageID, userID string) error

	// ResolveMessageReferences returns only active messages the caller can read
	// right now. Missing map entries are intentionally indistinguishable from
	// nonexistent, deleted, cross-workspace, and inaccessible origins.
	ResolveMessageReferences(ctx context.Context, workspaceID, userID string, messageIDs []string) (map[string]domain.MessageReference, error)
	// ListReferencedMessageIDs returns destination-message -> source-message IDs
	// only for active destination messages the caller can currently read. Source
	// IDs never leave the service boundary; they are resolved separately using
	// the caller's current access to each origin.
	ListReferencedMessageIDs(ctx context.Context, workspaceID, userID, channelID, dmConversationID string, messageIDs []string) (map[string]string, error)

	// ResolveMentionLabels returns current display labels keyed by "user:<uuid>"
	// or "channel:<uuid>", scoped to workspaceID.
	ResolveMentionLabels(ctx context.Context, workspaceID string, userIDs, channelIDs []string) (map[string]string, error)

	// ResolveAuthorizedMentionLabels returns labels only for references valid in
	// the source channel or group conversation for requesterID. CreateMessage
	// repeats this check atomically as the final authorization backstop.
	ResolveAuthorizedMentionLabels(ctx context.Context, workspaceID, sourceChannelID, sourceDMConversationID, requesterID string, userIDs, channelIDs []string) (map[string]string, error)

	// CountEligibleAllMentionRecipientsUpTo reports how many members a group
	// DM's @all currently resolves to (issue #776, SR-002) — active dm_members,
	// with active workspace membership, on an active and non-deleted account,
	// exactly the predicate CreateMessage's own eligible_all_mention_recipients
	// CTE applies.
	//
	// The count saturates at limit and stops reading membership there, so
	// deciding a bound never costs more than limit rows however large the group
	// is (SEC-776-01). It exists so a caller can refuse an over-bound @all with
	// a specific error before ever attempting the write; it is advisory, and
	// CreateMessage re-decides the same rule inside its own statement. That
	// re-decision is what catches a membership change committed between this
	// call and the write; a change committing strictly concurrently with the
	// write itself is ordinary read-committed visibility, not something either
	// side serializes against.
	CountEligibleAllMentionRecipientsUpTo(ctx context.Context, workspaceID, dmConversationID string, limit int) (int, error)

	// ListChannelMessages returns a paginated set of messages for a channel.
	// Visibility is enforced in SQL: active workspace, active workspace membership,
	// active channel, and private-channel membership are all required.
	// Returns an empty result when the channel is not visible to UserID.
	// Results are sorted oldest-first (ASC). NextCursor is nil when no older page exists.
	ListChannelMessages(ctx context.Context, input ListChannelMessagesInput) (ListMessagesResult, error)

	// ListDMMessages returns a paginated set of messages for a DM conversation.
	// Visibility is enforced in SQL: active workspace, active workspace membership,
	// active DM conversation, and active DM membership are all required.
	// Returns an empty result when the conversation is not visible to UserID.
	// Results are sorted oldest-first (ASC). NextCursor is nil when no older page exists.
	ListDMMessages(ctx context.Context, input ListDMMessagesInput) (ListMessagesResult, error)
}

// PGXMessageStore implements MessageStore using a pgx connection pool.
type PGXMessageStore struct {
	pool Pool
}

// NewPGXMessageStore creates a PGXMessageStore backed by the given pool.
func NewPGXMessageStore(pool Pool) *PGXMessageStore {
	return &PGXMessageStore{pool: pool}
}

// When alias is non-empty (e.g. "m"), columns are prefixed to avoid ambiguity
// in JOIN queries.
// withheldIfMalicious wraps a body-text expression so that a message whose links
// this deployment has condemned projects an empty body instead of its text
// (issue #135, CQ-002).
//
// # Why in SQL and not in the client
//
// The client already refuses to render a malicious body, but that is a decision
// taken over data it was nonetheless sent. Anything holding the response holds
// the URL: the network tab, a cached payload, a second client, a reload against
// a build that predates the check. "The reader does not see it" and "the reader
// cannot get it" are different guarantees, and only the second one is a block.
//
// # Why every projection, not just the main body
//
// A message is not shown in one place. It is quoted, referenced from another
// channel, and kept in edit history, and each of those is a separate query that
// reads body_text from the same row. Suppressing only the main body leaves the
// same string reachable through three other reads — which is the finding. This
// is the one expression all four go through, so a new projection that forgets it
// is a projection that does not compile against this helper.
//
// The state is read live from the source row rather than snapshotted, so a
// message condemned by background reconciliation *after* it was quoted is
// withheld from that quote on the next read, with no backfill of its own.
func withheldIfMalicious(bodyExpr, stateExpr string) string {
	return `CASE WHEN ` + stateExpr + ` = 'malicious' THEN '' ELSE ` + bodyExpr + ` END`
}

func messageColumns(alias string) string {
	p := ""
	if alias != "" {
		p = alias + "."
	}
	return p + `id::text, ` + p + `workspace_id::text,
	COALESCE(` + p + `channel_id::text, ''),
	COALESCE(` + p + `dm_conversation_id::text, ''),
	` + p + `sender_id::text,
	` + p + `kind,
	` + withheldIfMalicious(p+`body_text`, p+`link_safety_state`) + `,
	` + p + `body_format, ` + p + `status,
	COALESCE(` + p + `parent_message_id::text, ''),
	COALESCE(` + p + `forwarded_from_message_id::text, ''),
	COALESCE(` + p + `referenced_message_id::text, ''),
	` + p + `edited_at, ` + p + `edit_count, ` + p + `deleted_at,
	` + p + `created_at, ` + p + `updated_at,
	` + p + `link_safety_state,
	COALESCE(` + p + `event_type, ''),
	COALESCE(` + p + `event_payload, '{}'::jsonb)`
}

// listMessageColumns returns messageColumns plus sender display info from
// auth.users and the caller-scoped is_favorited flag. Requires a LEFT JOIN
// auth.users aliased as "u" in the query. userParam is the positional
// placeholder (e.g. "$3") holding the requesting user's ID — a code literal,
// never user input. Computing the flag inline keeps favorite marking a single
// index-backed query instead of a per-message lookup (RF-06 anti-N+1).
func listMessageColumns(alias, userParam string) string {
	return messageColumns(alias) + `,
	COALESCE(u.display_name, ''),
	COALESCE(u.email::text, ''),
	COALESCE(u.avatar_url, ''),
	EXISTS (
		SELECT 1 FROM chat.message_favorites mf
		WHERE mf.message_id = ` + alias + `.id AND mf.user_id = ` + userParam + `::uuid
	)`
}

func quotedMessageColumns(alias string) string {
	return `
	COALESCE(` + alias + `.id::text, ''),
	COALESCE(` + alias + `.sender_id::text, ''),
	COALESCE(` + withheldIfMalicious(alias+`.body_text`, alias+`.link_safety_state`) + `, ''),
	COALESCE(` + alias + `.body_format::text, ''),
	COALESCE(` + alias + `.status::text, ''),
	` + alias + `.deleted_at,
	` + alias + `.created_at,
	` + alias + `.updated_at,
	COALESCE(` + alias + `.link_safety_state, '')`
}

func listMessageWithQuoteColumns(alias, userParam, quoteAlias string) string {
	return listMessageColumns(alias, userParam) + `,` + quotedMessageColumns(quoteAlias)
}

func quotedMessageJoin(alias, quoteAlias string) string {
	return `
		LEFT JOIN chat.messages ` + quoteAlias + `
		  ON ` + quoteAlias + `.id = ` + alias + `.parent_message_id
		 AND ` + quoteAlias + `.workspace_id = ` + alias + `.workspace_id
		 AND ` + quoteAlias + `.channel_id IS NOT DISTINCT FROM ` + alias + `.channel_id
		 AND ` + quoteAlias + `.dm_conversation_id IS NOT DISTINCT FROM ` + alias + `.dm_conversation_id`
}

// scanMessageWithSenderAndQuote reads a single message row including sender
// display info and optional quote preview. It must be called with exactly the
// columns listed in listMessageWithQuoteColumns.
func scanMessageWithSenderAndQuote(row pgx.Row) (domain.Message, error) {
	return scanMessageWithSenderAndQuoteExtra(row)
}

func scanMessageWithSenderAndQuoteExtra(row pgx.Row, extra ...any) (domain.Message, error) {
	var msg domain.Message
	var editedAt, deletedAt *time.Time
	var quote domain.QuotedMessage
	var quoteDeletedAt, quoteCreatedAt, quoteUpdatedAt *time.Time
	var eventPayload []byte
	destinations := []any{
		&msg.ID, &msg.WorkspaceID,
		&msg.ChannelID, &msg.DMConversationID,
		&msg.SenderID,
		(*string)(&msg.Kind), &msg.BodyText, (*string)(&msg.BodyFormat), (*string)(&msg.Status),
		&msg.ParentMessageID, &msg.ForwardedFromMessageID, &msg.ReferencedMessageID,
		&editedAt, &msg.EditCount, &deletedAt,
		&msg.CreatedAt, &msg.UpdatedAt,
		(*string)(&msg.LinkSafety),
		&msg.EventType, &eventPayload,
		&msg.SenderDisplayName, &msg.SenderEmail, &msg.SenderAvatarURL,
		&msg.IsFavorited,
		&quote.ID, &quote.AuthorID, &quote.BodyText, (*string)(&quote.BodyFormat), (*string)(&quote.Status),
		&quoteDeletedAt, &quoteCreatedAt, &quoteUpdatedAt, (*string)(&quote.LinkSafety),
	}
	destinations = append(destinations, extra...)
	err := row.Scan(destinations...)
	if err != nil {
		return domain.Message{}, err
	}
	if err := decodeConversationEvent(&msg, eventPayload); err != nil {
		return domain.Message{}, err
	}
	if editedAt != nil {
		msg.EditedAt = *editedAt
	}
	if deletedAt != nil {
		msg.DeletedAt = *deletedAt
	}
	if quote.ID != "" {
		if quoteDeletedAt != nil {
			quote.DeletedAt = *quoteDeletedAt
		}
		if quoteCreatedAt != nil {
			quote.CreatedAt = *quoteCreatedAt
		}
		if quoteUpdatedAt != nil {
			quote.UpdatedAt = *quoteUpdatedAt
		}
		msg.Quoted = &quote
	}
	return msg, nil
}

// nullableUUID converts an empty string to nil for pgx nullable UUID parameters.
func nullableUUID(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func (s *PGXMessageStore) CreateMessage(ctx context.Context, input CreateMessageInput) (domain.Message, error) {
	kind := input.Kind
	if kind == "" {
		kind = domain.MessageKindUser
	}
	bodyFormat := input.BodyFormat
	if bodyFormat == "" {
		bodyFormat = domain.MessageBodyFormatV1
	}
	maxAttachmentBytes := input.MaxAttachmentBytes
	if maxAttachmentBytes <= 0 {
		maxAttachmentBytes = domain.DefaultMaxMessageAttachmentBytes
	}
	// Authorization and reference integrity are enforced atomically in one INSERT.
	//
	// The auth subquery (UNION ALL of channel branch + DM branch) yields exactly one
	// row only when the sender is authorized at insert time:
	//   channel branch ($2 IS NOT NULL):
	//     - workspace active, sender is active workspace_member
	//     - channel belongs to workspace, channel is active
	//     - public channel: active workspace member is sufficient
	//     - private channel: sender must also be an active channel_member
	//   DM branch ($3 IS NOT NULL):
	//     - workspace active, sender is active workspace_member
	//     - DM conversation belongs to workspace, DM conversation is active
	//     - sender must be an active dm_member
	//
	// Stale channel_members / dm_members cannot bypass an inactive/suspended/left
	// workspace_member because the workspace_members JOIN filters wm.status = 'active'
	// independently.
	//
	// The invalid_refs CTE keeps parent/forwarded references in the same target and
	// permits RF-09 referenced messages in another target only when the sender can
	// currently read the active origin. Any failure maps to the same zero-row result.
	//
	// The INSERT is wrapped in a CTE so the outer SELECT can JOIN auth.users and
	// return sender display info (sender_display_name, sender_email) in the same
	// round-trip. This avoids a separate GET after insert for the broadcast payload.
	row := s.pool.QueryRow(ctx, `
		WITH user_mentions AS (
			SELECT DISTINCT id::uuid AS user_id
			FROM unnest($11::text[]) AS ids(id)
		),
		channel_mentions AS (
			SELECT DISTINCT id::uuid AS channel_id
			FROM unnest($12::text[]) AS ids(id)
		),
			attachment_candidates AS (
				SELECT id::uuid AS attachment_id, ord
				FROM unnest($13::text[]) WITH ORDINALITY AS ids(id, ord)
			),
			authorized_attachments AS MATERIALIZED (
				SELECT a.id AS attachment_id, a.size_bytes, candidate.ord
				FROM attachment_candidates candidate
				JOIN files.attachments a ON a.id = candidate.attachment_id
				WHERE a.workspace_id = $1::uuid
				  AND a.channel_id IS NOT DISTINCT FROM $2::uuid
				  AND a.conversation_id IS NOT DISTINCT FROM $3::uuid
				  AND a.uploader_id = $4::uuid
				  AND a.status IN ('pending_scan', 'clean')
				  AND a.deleted_at IS NULL
				  AND NOT EXISTS (
					SELECT 1 FROM chat.message_attachments existing
					WHERE existing.attachment_id = a.id
				  )
				FOR UPDATE OF a
			),
			-- RF-32 attachment authorization. A client sends only ids, so every
			-- property that decides whether a link may exist is re-read here from
			-- files.attachments — never taken from the request:
			--   - the attachment exists and is not soft-deleted;
			--   - it belongs to this message's workspace;
			--   - it belongs to exactly this message's destination. The IS NOT
			--     DISTINCT FROM pair is what makes that one test instead of four:
			--     exactly one of $2/$3 is non-null, and files.attachments has its
			--     own exclusivity CHECK, so a channel attachment cannot match a DM
			--     message, a DM attachment cannot match a channel message, and
			--     neither can match another channel or another conversation;
			--   - it was uploaded by this sender. Being able to read a destination
			--     is not permission to post somebody else's upload as one's own;
			--   - its scan state is one a link may be made in. pending_scan is
			--     deliberately allowed: the scan is asynchronous by design and a
			--     message must be sendable while it runs. pending_upload (never
			--     finished), rejected, failed and deleted are all refused;
			--   - it is not already bound to a message, which is the same rule the
			--     UNIQUE constraint enforces, checked here so a replay is a plain
			--     zero-row refusal rather than a constraint error.
			-- Any failure yields a row here, which stops the INSERT, which the
			-- caller reports as the same non-enumerating not-found every other
			-- invalid reference produces.
			invalid_attachments AS (
				SELECT 1
				WHERE (SELECT COUNT(*) FROM authorized_attachments) <>
				      (SELECT COUNT(*) FROM attachment_candidates)
				   OR (SELECT COALESCE(SUM(size_bytes), 0) FROM authorized_attachments) > $20::bigint
			),
			invalid_refs AS (
			SELECT 1 FROM (VALUES (1)) v(x)
			WHERE
				($8::uuid IS NOT NULL AND NOT EXISTS (
					SELECT 1 FROM chat.messages
					WHERE id = $8::uuid
					  AND workspace_id = $1::uuid
					  AND status = 'active'
					  AND channel_id IS NOT DISTINCT FROM $2::uuid
					  AND dm_conversation_id IS NOT DISTINCT FROM $3::uuid
				))
				OR ($9::uuid IS NOT NULL AND NOT EXISTS (
					SELECT 1 FROM chat.messages
					WHERE id = $9::uuid
					  AND workspace_id = $1::uuid
					  AND status = 'active'
					  AND channel_id IS NOT DISTINCT FROM $2::uuid
					  AND dm_conversation_id IS NOT DISTINCT FROM $3::uuid
				))
				OR ($10::uuid IS NOT NULL AND NOT EXISTS (
					SELECT 1 FROM chat.messages m`+messageAccessJoins("$4")+`
					WHERE m.id = $10::uuid
					  AND m.workspace_id = $1::uuid
					  AND m.status = 'active'
					  AND m.deleted_at IS NULL
					  AND NOT (
						m.channel_id IS NOT DISTINCT FROM $2::uuid
						AND m.dm_conversation_id IS NOT DISTINCT FROM $3::uuid
					  )
					  AND `+messageAccessPredicate("$4")+`
				))
		),
		invalid_mentions AS (
			SELECT 1
			FROM user_mentions um
			WHERE NOT EXISTS (
				SELECT 1
				FROM chat.channels source_channel
				JOIN chat.channel_members cm
				  ON cm.channel_id = source_channel.id AND cm.user_id = um.user_id
				JOIN chat.workspace_members mentioned_member
				  ON mentioned_member.workspace_id = source_channel.workspace_id
				 AND mentioned_member.user_id = um.user_id
				 AND mentioned_member.status = 'active'
				JOIN auth.users mentioned_user
				  ON mentioned_user.id = um.user_id
				 AND mentioned_user.status = 'active'
				 AND mentioned_user.deleted_at IS NULL
				WHERE $2::uuid IS NOT NULL
				  AND source_channel.id = $2::uuid
				  AND source_channel.workspace_id = $1::uuid
				  AND source_channel.status = 'active'
				UNION ALL
				SELECT 1
				FROM chat.dm_conversations source_dm
				JOIN chat.dm_members mentioned_dm
				  ON mentioned_dm.conversation_id = source_dm.id
				 AND mentioned_dm.user_id = um.user_id
				 AND mentioned_dm.status = 'active'
				JOIN chat.workspace_members mentioned_member
				  ON mentioned_member.workspace_id = source_dm.workspace_id
				 AND mentioned_member.user_id = um.user_id
				 AND mentioned_member.status = 'active'
				JOIN auth.users mentioned_user
				  ON mentioned_user.id = um.user_id
				 AND mentioned_user.status = 'active'
				 AND mentioned_user.deleted_at IS NULL
				WHERE $3::uuid IS NOT NULL
				  AND source_dm.id = $3::uuid
				  AND source_dm.workspace_id = $1::uuid
				  AND source_dm.type = 'group'
				  AND source_dm.status = 'active'
			)
			UNION ALL
			SELECT 1
			FROM channel_mentions mentioned
			WHERE $2::uuid IS NULL OR NOT EXISTS (
				SELECT 1
				FROM chat.channels c
				JOIN chat.workspaces w
				  ON w.id = c.workspace_id AND w.status = 'active'
				JOIN chat.workspace_members requester
				  ON requester.workspace_id = c.workspace_id
				 AND requester.user_id = $4::uuid
				 AND requester.status = 'active'
				WHERE c.id = mentioned.channel_id
				  AND c.workspace_id = $1::uuid
				  AND c.status = 'active'
				  AND chat.channel_visible_to_user(c.id, $4::uuid)
			)
		),
		-- issue #776: @all in a group DM. Recipients are never taken from the
		-- service layer's own list — that would trust a set computed before this
		-- statement ran, so a change that already committed by the time the send
		-- reaches the database (a member removed, or added, before this request
		-- was even issued) would be missed entirely. Instead this CTE reads
		-- chat.dm_members itself, inside the same statement as the INSERT, under
		-- that statement's own read-committed snapshot — which is what makes a
		-- membership change that committed *before* this statement began always
		-- visible here, regardless of what the client's autocomplete or composer
		-- last saw.
		--
		-- This is not a claim of serializable isolation against a write racing
		-- this exact statement's own execution: dm_members/workspace_members are
		-- read here with no row lock (no FOR UPDATE/FOR SHARE), so a removal that
		-- commits strictly concurrently with this INSERT is ordinary Postgres
		-- read-committed visibility, not something this design serializes
		-- against. #776's requirement is "the source of truth beats a value the
		-- client is holding," not "this send blocks on every in-flight
		-- membership write" — locking dm_members here would be a real behavior
		-- and performance change with no stated requirement driving it, so none
		-- was added speculatively.
		--
		-- $21 is the one bit the service contributes: whether the body carries an
		-- "all" token in a target the service already confirmed is a group. The
		-- dc.type = 'group' condition re-asserts that authoritatively rather than
		-- trusting the flag alone — a DM cannot actually change type, but the
		-- guard costs nothing and keeps this CTE correct on its own terms. This
		-- reads $1/$3 (the request's own target parameters) rather than
		-- inserted.workspace_id/inserted.dm_conversation_id deliberately: it must
		-- be computable *before* the INSERT below runs, so the INSERT's own WHERE
		-- clause can refuse to write a message at all when the fan-out is over
		-- the SR-002 bound (see invalid_all_mention_fanout).
		-- When $21 is false the WHERE eliminates every row, so a channel send (or
		-- any DM send without @all) reaches this CTE and produces nothing —
		-- byte-for-byte the same mention_outbox/pending_mentions rows as before
		-- this feature existed.
		--
		-- SEC-776-01: the LIMIT is the whole point of this CTE's shape. Reading
		-- one row past the bound is everything either decision needs — at most
		-- $22 rows means "send it, and these are exactly the recipients", one
		-- more means "refuse", and neither answer improves by walking the rest of
		-- a 50,000-member roster. So the database stops at $22 + 1 rows, and an
		-- oversized group costs the same as a group of 51.
		--
		-- No DISTINCT: chat.dm_members is keyed (conversation_id, user_id) and
		-- both joins below are on primary keys, so an eligible member already
		-- yields exactly one row. Dropping it is what lets the LIMIT stop early
		-- rather than forcing a dedupe across the whole roster first.
		eligible_all_mention_recipients AS (
			SELECT dm.user_id
			FROM chat.dm_conversations dc
			JOIN chat.dm_members dm
			  ON dm.conversation_id = dc.id
			 AND dm.status = 'active'
			JOIN chat.workspace_members wm
			  ON wm.workspace_id = dc.workspace_id
			 AND wm.user_id = dm.user_id
			 AND wm.status = 'active'
			JOIN auth.users u
			  ON u.id = dm.user_id
			 AND u.status = 'active'
			 AND u.deleted_at IS NULL
			WHERE $21::boolean
			  AND $3::uuid IS NOT NULL
			  AND dc.id = $3::uuid
			  AND dc.workspace_id = $1::uuid
			  AND dc.type = 'group'
			  AND dc.status = 'active'
			LIMIT $22::int + 1
		),
		-- issue #776 SR-002: a broadcast is either sent to everyone it resolves to
		-- or sent to no one — never truncated to an arbitrary first-N subset,
		-- which would silently misinform an author about who actually saw an
		-- @all they wrote for a larger audience. Gating the INSERT's own WHERE
		-- clause on this is what makes "too large" a whole-message refusal (zero
		-- rows: no message, no outbox, no partial fan-out) rather than a value
		-- decided after the row already exists.
		--
		-- It asks the one question that decides the bound — "is there a row past
		-- the limit?" — as an existence test over the already-capped CTE above,
		-- not as a count over the roster. OFFSET $22 skips the rows a legal @all
		-- may have; anything still standing is the $22+1'th eligible recipient
		-- and refuses the message. Which row that is does not matter and is not
		-- ordered: only whether one exists.
		--
		-- $22 is domain.MaxGroupAllMentionRecipients, passed as a parameter
		-- rather than inlined so the Go constant stays the only place the limit
		-- is spelled out — the "+ 1" above derives from it rather than repeating
		-- a second literal.
		invalid_all_mention_fanout AS (
			SELECT 1
			FROM eligible_all_mention_recipients
			OFFSET $22::int
			LIMIT 1
		),
		-- The recipients an accepted @all actually notifies. Reading from the
		-- capped CTE (never a second pass over membership) and refusing to yield
		-- anything at all once the bound is broken, so the "all or nobody" rule
		-- holds here on its own terms rather than only as a consequence of the
		-- INSERT below writing no row.
		all_mention_recipients AS (
			SELECT user_id
			FROM eligible_all_mention_recipients
			WHERE NOT EXISTS (SELECT 1 FROM invalid_all_mention_fanout)
		),
		inserted AS (
			INSERT INTO chat.messages
				(workspace_id, channel_id, dm_conversation_id, sender_id,
				 kind, body_text, body_format, status,
				 parent_message_id, forwarded_from_message_id, referenced_message_id,
				 create_idempotency_key, create_request_fingerprint, link_safety_state,
				 link_safety_fingerprint,
				 link_safety_projection_version)
			SELECT $1::uuid, $2::uuid, $3::uuid, $4::uuid, $5, $6, $7, $14::text,
			       $8::uuid, $9::uuid, $10::uuid, NULLIF($16, ''), NULLIF($18, ''), $19::text,
			       NULLIF($17, ''), 1
			FROM (
				-- Channel message authorization branch.
				SELECT 1
				FROM chat.workspaces w
				JOIN chat.workspace_members wm
				  ON wm.workspace_id = w.id AND wm.user_id = $4::uuid AND wm.status = 'active'
				JOIN chat.channels c
				  ON c.id = $2::uuid AND c.workspace_id = $1::uuid AND c.status = 'active'
				WHERE $2::uuid IS NOT NULL
				  AND w.id = $1::uuid AND w.status = 'active'
				  AND chat.channel_visible_to_user(c.id, $4::uuid)
				UNION ALL
				-- DM message authorization branch.
				SELECT 1
				FROM chat.workspaces w
				JOIN chat.workspace_members wm
				  ON wm.workspace_id = w.id AND wm.user_id = $4::uuid AND wm.status = 'active'
				JOIN chat.dm_conversations dc
				  ON dc.id = $3::uuid AND dc.workspace_id = $1::uuid AND dc.status = 'active'
				JOIN chat.dm_members dm
				  ON dm.conversation_id = dc.id AND dm.user_id = $4::uuid AND dm.status = 'active'
				WHERE $3::uuid IS NOT NULL
				  AND w.id = $1::uuid AND w.status = 'active'
			) auth
			WHERE NOT EXISTS (SELECT 1 FROM invalid_refs)
			  AND NOT EXISTS (SELECT 1 FROM invalid_mentions)
			  AND NOT EXISTS (SELECT 1 FROM invalid_attachments)
			  AND NOT EXISTS (SELECT 1 FROM invalid_all_mention_fanout)
			RETURNING id, workspace_id, channel_id, dm_conversation_id, sender_id,
				          kind, body_text, body_format, status,
			          parent_message_id, forwarded_from_message_id, referenced_message_id,
			          edited_at, edit_count, deleted_at, created_at, updated_at,
			          link_safety_state,
			          -- Always NULL here: CreateMessage writes user messages, and a
			          -- system message is written by its own event path. The columns
			          -- are still projected because the outer SELECT reads this CTE
			          -- through messageColumns, which names them (issue #527).
			          event_type, event_payload
		),
		-- The set an @all in a group DM notifies: everyone individually mentioned,
		-- plus everyone @all resolved to, counted once each — so an author who
		-- writes "@all @[Ana](...)" or two "@all" tokens in the same message
		-- never produces two outbox rows for the same person.
		mention_recipients AS (
			SELECT user_id FROM user_mentions
			UNION
			SELECT user_id FROM all_mention_recipients
		),
		-- A published message notifies its mentions immediately, exactly as it
		-- always has. A withheld one must not: a notification is a side effect
		-- aimed at somebody who is not allowed to know the message exists yet,
		-- and RF-21's rule is that a pending message produces none of those.
		mention_outbox AS (
			INSERT INTO chat.notification_outbox
				(workspace_id, message_id, recipient_user_id, kind, status)
			SELECT inserted.workspace_id, inserted.id, mention_recipients.user_id, 'mention', 'pending'
			FROM inserted
			CROSS JOIN mention_recipients
			WHERE inserted.status = 'active'
			ON CONFLICT (message_id, recipient_user_id, kind) DO NOTHING
			RETURNING id
		),
		-- Dropping the mentions instead would lose them for good once the scan
		-- cleared, so they are parked and released by the promotion, in the same
		-- transaction that makes the message publishable.
		pending_mentions AS (
			INSERT INTO chat.message_pending_mentions (message_id, user_id)
			SELECT inserted.id, mention_recipients.user_id
			FROM inserted
			CROSS JOIN mention_recipients
			WHERE inserted.status = 'pending_link_scan'
			ON CONFLICT DO NOTHING
			RETURNING message_id
		),
		-- Same statement as the INSERT above, so the message and its links are one
		-- atomic fact: there is no commit in which one exists without the other.
		-- It reads the inserted CTE, so it writes nothing when authorization or
		-- any reference check produced no message.
		attachment_links AS (
			INSERT INTO chat.message_attachments (message_id, attachment_id, position)
			SELECT inserted.id, candidate.attachment_id, candidate.ord - 1
			FROM inserted
			CROSS JOIN authorized_attachments candidate
			RETURNING attachment_id
		),
		published_attachments AS (
			UPDATE files.attachments AS a
			SET draft_expires_at = NULL, updated_at = now()
			FROM attachment_links AS linked
			WHERE a.id = linked.attachment_id
			RETURNING a.id
		),
		-- RF-21. Same statement again, for the same reason: a withheld message
		-- and the URLs it is waiting on are one atomic fact. A commit in which
		-- the message exists without its links would be a message nothing can
		-- ever promote — held forever with no verdict to wait for.
		--
		-- The chat.link_scans rows these reference are created before the
		-- statement runs, by EnsureLinkScans, which is idempotent; a row that
		-- ends up with no message is simply a URL that gets scanned once.
		link_scan_links AS (
			INSERT INTO chat.message_link_scans (message_id, canonical_url, fingerprint)
			SELECT inserted.id, url, COALESCE(NULLIF($17, ''), '')
			FROM inserted
			CROSS JOIN unnest($15::text[]) AS urls(url)
			ON CONFLICT DO NOTHING
			RETURNING canonical_url
		)
		SELECT `+listMessageWithQuoteColumns("m", "$4", "q")+`
		FROM inserted m
		LEFT JOIN auth.users u ON u.id = m.sender_id`+quotedMessageJoin("m", "q"),
		input.WorkspaceID,
		nullableUUID(input.ChannelID),
		nullableUUID(input.DMConversationID),
		input.SenderID,
		string(kind),
		input.BodyText,
		string(bodyFormat),
		nullableUUID(input.ParentMessageID),
		nullableUUID(input.ForwardedFromMessageID),
		nullableUUID(input.ReferencedMessageID),
		input.MentionedUserIDs,
		input.MentionedChannelIDs,
		input.AttachmentIDs,
		string(messageStatusOrActive(input.Status)),
		input.LinkScanURLs,
		input.IdempotencyKey,
		input.LinkSafetyFingerprint,
		input.RequestFingerprint,
		string(input.LinkSafetyState),
		maxAttachmentBytes,
		input.MentionAllGroupMembers,
		domain.MaxGroupAllMentionRecipients,
	)
	msg, err := scanMessageWithSenderAndQuote(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Non-enumerating TOCTOU backstop: auth failure, reference failure and
			// attachment failure all produce 0 rows. The service layer returns typed
			// errors from pre-validation; this backstop returns ErrNotFound to avoid
			// leaking target existence — or which attachment ids exist.
			return domain.Message{}, domain.ErrNotFound
		}
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) {
			switch pgErr.Code {
			case "23505", "23503": // unique_violation, foreign_key_violation
				// A collision on the idempotency index is not a failure: it is a
				// concurrent retry of this very send, and the caller resolves it
				// by reading back what the winner created. Every other unique
				// violation keeps the non-enumerating answer — the attachment may
				// have been linked between invalid_attachments and
				// attachment_links, and that race must stay indistinguishable.
				if pgErr.ConstraintName == createIdempotencyConstraint {
					return domain.Message{}, ErrCreateReplay
				}
				return domain.Message{}, domain.ErrNotFound
			case "23514", "23502": // check_violation, not_null_violation
				return domain.Message{}, domain.ErrInvalidInput
			}
		}
		return domain.Message{}, fmt.Errorf("create message: %w", err)
	}
	// The links were written by a CTE of the statement above, which cannot see its
	// own effects, so the metadata is read back here — one query, never one per
	// attachment.
	if len(input.AttachmentIDs) > 0 {
		messages := []domain.Message{msg}
		if err := s.loadAttachmentBatch(ctx, messages); err != nil {
			return domain.Message{}, err
		}
		msg = messages[0]
	}
	return msg, nil
}

// SnapshotForwardableMessage reads what a forward would copy.
//
// The source-side predicates are character-for-character the ones the forwarding
// statement applies, so a source this refuses is a source that would have been
// refused there, with the same non-enumerating ErrNotFound. It is a plain read:
// no transaction, no FOR SHARE, no reserved connection — the caller runs a
// third-party lookup on the result, and holding a row lock across a network call
// to Cloudflare is exactly what must not happen.
func (s *PGXMessageStore) SnapshotForwardableMessage(
	ctx context.Context, input ForwardSnapshotInput,
) (ForwardSnapshot, error) {
	var snapshot ForwardSnapshot
	err := s.pool.QueryRow(ctx, `
		SELECT m.id::text, m.body_text, m.body_format
		FROM chat.messages m`+messageAccessJoins("$3")+`
		WHERE m.id = $4::uuid
		  AND m.workspace_id = $1::uuid
		  AND m.channel_id IS NOT NULL
		  AND m.channel_id <> $2::uuid
		  AND m.kind = 'user'
		  AND m.status = 'active'
		  AND m.deleted_at IS NULL
		  AND `+messageAccessPredicate("$3"),
		input.WorkspaceID, input.DestinationChannelID, input.ActorID, input.SourceMessageID,
	).Scan(&snapshot.SourceMessageID, &snapshot.BodyText, (*string)(&snapshot.BodyFormat))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ForwardSnapshot{}, domain.ErrNotFound
		}
		return ForwardSnapshot{}, fmt.Errorf("snapshot forwardable message: %w", err)
	}
	return snapshot, nil
}

// LookupForwardReplay answers "has this forward already happened?" without
// writing anything.
//
// It exists so a retried forward is resolved *before* the RF-21 provider call.
// Checking first and looking afterwards made a legitimate retry depend on
// Cloudflare agreeing twice: a verdict that changed, or a provider that went
// down, turned a replay of an already-persisted message into a refusal, which
// is not what an idempotency key means.
//
// The predicate is the ON CONFLICT key — workspace, sender, channel, key — so a
// row this finds is exactly the row the upsert would have returned. The
// conflict rule is the same one, applied here instead of after an INSERT.
//
// One predicate is deliberately *not* repeated: the forwarding statement also
// requires the source to still be readable, and this does not. The message it
// returns is already in the destination channel and the caller is already
// authorized to read that channel — the same access predicate every message
// read applies — so losing access to the original afterwards does not make a
// message the caller can list anyway into something withheld. What it must not
// do is hand the row to someone else, and the sender and channel predicates are
// what prevent that.
func (s *PGXMessageStore) LookupForwardReplay(ctx context.Context, input ForwardReplayInput) (domain.Message, error) {
	if input.IdempotencyKey == "" {
		return domain.Message{}, domain.ErrNotFound
	}
	row := s.pool.QueryRow(ctx, `
		SELECT `+listMessageWithQuoteColumns("m", "$3", "q")+`
		FROM chat.messages m`+messageAccessJoins("$3")+`
		LEFT JOIN auth.users u ON u.id = m.sender_id`+quotedMessageJoin("m", "q")+`
		WHERE m.workspace_id = $1::uuid
		  AND m.channel_id = $2::uuid
		  AND m.sender_id = $3::uuid
		  AND m.forward_idempotency_key = $4
		  AND `+messageAccessPredicate("$3"),
		input.WorkspaceID, input.DestinationChannelID, input.ActorID, input.IdempotencyKey,
	)
	msg, err := scanMessageWithSenderAndQuote(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.Message{}, domain.ErrNotFound
		}
		return domain.Message{}, fmt.Errorf("lookup forward replay: %w", err)
	}
	if msg.ForwardedFromMessageID != input.SourceMessageID {
		return domain.Message{}, domain.ErrConflict
	}
	messages := []domain.Message{msg}
	if err := s.loadReactionBatch(ctx, messages, input.ActorID); err != nil {
		return domain.Message{}, err
	}
	if err := s.loadAttachmentBatch(ctx, messages); err != nil {
		return domain.Message{}, err
	}
	return messages[0], nil
}

func (s *PGXMessageStore) ForwardChannelMessage(ctx context.Context, input ForwardChannelMessageInput) (ForwardChannelMessageResult, error) {
	// This CTE is the authoritative authorization and consistency control.
	// Keep source and destination predicates in this atomic statement; zero rows
	// deliberately maps every inaccessible or invalid resource to ErrNotFound.
	//
	// The source row is still required to exist and be accessible — that is the
	// authorization — but its body is no longer what gets written. The INSERT
	// writes the snapshot in $6/$7, which the caller has already checked, so the
	// content that was validated and the content that is persisted are the same
	// bytes even if the source is edited in between (RF-21).
	row := s.pool.QueryRow(ctx, `
		WITH source AS (
			SELECT m.id
			FROM chat.messages m`+messageAccessJoins("$3")+`
			WHERE m.id = $4::uuid
			  AND m.workspace_id = $1::uuid
			  AND m.channel_id IS NOT NULL
			  AND m.channel_id <> $2::uuid
			  AND m.kind = 'user'
			  AND m.status = 'active'
			  AND m.deleted_at IS NULL
			  AND `+messageAccessPredicate("$3")+`
			FOR SHARE OF m
		),
		inserted AS (
			INSERT INTO chat.messages
				(workspace_id, channel_id, sender_id, kind, body_text, body_format,
				 status, forwarded_from_message_id, forward_idempotency_key,
				 link_safety_state, link_safety_fingerprint, link_safety_projection_version)
			SELECT $1::uuid, $2::uuid, $3::uuid, 'user', $6::text,
			       $7::text, $8::text, source.id, NULLIF($5, ''), $11::text, NULLIF($10, ''), 1
			FROM source
			JOIN chat.workspaces destination_workspace
			  ON destination_workspace.id = $1::uuid AND destination_workspace.status = 'active'
			JOIN chat.workspace_members destination_member
			  ON destination_member.workspace_id = destination_workspace.id
			 AND destination_member.user_id = $3::uuid
			 AND destination_member.status = 'active'
			JOIN chat.channels destination_channel
			  ON destination_channel.id = $2::uuid
			 AND destination_channel.workspace_id = destination_workspace.id
			 AND destination_channel.status = 'active'
			WHERE chat.channel_visible_to_user(destination_channel.id, $3::uuid)
			ON CONFLICT (workspace_id, sender_id, channel_id, forward_idempotency_key)
				WHERE forward_idempotency_key IS NOT NULL
			DO UPDATE SET forward_idempotency_key = EXCLUDED.forward_idempotency_key
			RETURNING id, workspace_id, channel_id, dm_conversation_id, sender_id,
			          kind, body_text, body_format, status,
			          parent_message_id, forwarded_from_message_id, referenced_message_id,
			          edited_at, edit_count, deleted_at, created_at, updated_at,
			          link_safety_state,
			          -- Always NULL here: a forward is a user message. The columns
			          -- are still projected because the outer SELECT reads this CTE
			          -- through messageColumns, which names them (issue #527).
			          event_type, event_payload,
			          (xmax <> 0) AS replayed
		),
		-- RF-21, same atomicity argument as CreateMessage's: the withheld
		-- forward and the URLs it waits on are one commit. ON CONFLICT DO
		-- NOTHING makes a replay a no-op here too — the edges are already there
		-- from the original insert.
		link_scan_links AS (
			INSERT INTO chat.message_link_scans (message_id, canonical_url, fingerprint)
			SELECT inserted.id, url, COALESCE(NULLIF($10, ''), '')
			FROM inserted
			CROSS JOIN unnest($9::text[]) AS urls(url)
			ON CONFLICT DO NOTHING
			RETURNING canonical_url
		)
		SELECT `+listMessageWithQuoteColumns("m", "$3", "q")+`, m.replayed
		FROM inserted m
		LEFT JOIN auth.users u ON u.id = m.sender_id`+quotedMessageJoin("m", "q"),
		input.WorkspaceID, input.DestinationChannelID, input.ActorID, input.SourceMessageID,
		input.IdempotencyKey, input.BodyText, string(input.BodyFormat),
		string(messageStatusOrActive(input.Status)), input.LinkScanURLs,
		input.LinkSafetyFingerprint, string(input.LinkSafetyState),
	)
	var replayed bool
	msg, err := scanMessageWithSenderAndQuoteExtra(row, &replayed)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ForwardChannelMessageResult{}, domain.ErrNotFound
		}
		return ForwardChannelMessageResult{}, fmt.Errorf("forward channel message storage: %w", err)
	}
	if replayed && msg.ForwardedFromMessageID != input.SourceMessageID {
		return ForwardChannelMessageResult{}, domain.ErrConflict
	}
	return ForwardChannelMessageResult{Message: msg, Replayed: replayed}, nil
}

// EditMessage atomically snapshots the current body and replaces it after
// server-side access, author, deletion, and edit-window validation.
func (s *PGXMessageStore) EditMessage(ctx context.Context, input EditMessageInput) (domain.Message, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.Message{}, fmt.Errorf("begin message edit: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var current domain.Message
	var deletedAt *time.Time
	var editWindowSeconds *int
	var databaseNow time.Time
	err = tx.QueryRow(ctx, `
		SELECT m.sender_id::text, m.kind, m.status, m.deleted_at, m.created_at,
		       w.edit_window_seconds, clock_timestamp()
		FROM chat.messages m`+messageAccessJoins("$3")+`
		WHERE m.workspace_id = $1 AND m.id = $2
		  AND `+messageAccessPredicate("$3")+`
		FOR UPDATE OF m`,
		input.WorkspaceID, input.MessageID, input.EditorID,
	).Scan(&current.SenderID, (*string)(&current.Kind), (*string)(&current.Status), &deletedAt, &current.CreatedAt, &editWindowSeconds, &databaseNow)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.Message{}, domain.ErrNotFound
		}
		return domain.Message{}, fmt.Errorf("lock editable message: %w", err)
	}
	if deletedAt != nil {
		current.DeletedAt = *deletedAt
	}
	if err := domain.ValidateMessageEdit(current, input.EditorID, editWindowSeconds, databaseNow); err != nil {
		return domain.Message{}, err
	}

	var historyID string
	err = tx.QueryRow(ctx, `
		WITH snapshot AS (
			INSERT INTO chat.message_edit_history
				(message_id, body, body_format, editor_user_id, versioned_at, link_safety_fingerprint)
			SELECT id, body_text, body_format, $2, $3,
			       CASE WHEN link_safety_projection_version > 0
			            THEN COALESCE(link_safety_fingerprint, '') END
			FROM chat.messages
			WHERE id = $1
			RETURNING id, message_id, link_safety_fingerprint
		), linked AS (
			INSERT INTO chat.message_edit_history_link_scans (history_id, canonical_url)
			SELECT snapshot.id, mls.canonical_url
			FROM snapshot
			JOIN chat.message_link_scans mls
			  ON mls.message_id = snapshot.message_id
			 AND mls.fingerprint = snapshot.link_safety_fingerprint
		)
		SELECT id::text FROM snapshot`, input.MessageID, input.EditorID, databaseNow).Scan(&historyID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.Message{}, domain.ErrNotFound
		}
		return domain.Message{}, fmt.Errorf("snapshot message edit: %w", err)
	}

	// The link-safety half of the edit, in the same transaction as the body
	// (issue #135, CQ-001).
	//
	// The invariant this establishes: once this transaction commits, the state and
	// associations selected by the message's current fingerprint describe the new
	// body and nothing else. Prior fingerprints remain only as edit-history
	// redaction evidence and cannot decide the current row.
	//
	// Re-checked here rather than trusted from the caller. The service classified
	// the new body before this transaction opened, and a reconciliation could have
	// landed in between; the row is held FOR UPDATE, so checking now closes that
	// window. A URL that has become malicious, or that has lost its terminal state,
	// refuses the edit exactly as it would have refused it a moment earlier.
	if err := assertEditableLinkStates(ctx, tx, input.LinkScanURLs); err != nil {
		return domain.Message{}, err
	}
	// Prior URLs were copied to the edit-history association table above. The
	// current table now describes only the new body.
	// Every current-body reader joins on messages.link_safety_fingerprint, so an
	// old association can redact its old version but cannot decide the new body.
	// The fingerprint exists only to bind associations to the body version they
	// were extracted from, so a body with no URLs must not keep one. Derived here
	// rather than trusted from the caller: a leftover fingerprint with no rows to
	// match is precisely the stale link fact this transaction exists to prevent,
	// and the store is the last place that can still refuse it.
	fingerprint := input.LinkSafetyFingerprint
	if _, err := tx.Exec(ctx,
		`DELETE FROM chat.message_link_scans WHERE message_id = $1`, input.MessageID,
	); err != nil {
		return domain.Message{}, fmt.Errorf("replace message link scans: %w", err)
	}
	if len(input.LinkScanURLs) == 0 {
		fingerprint = ""
	} else {
		if _, err := tx.Exec(ctx, `
			INSERT INTO chat.message_link_scans (message_id, canonical_url, fingerprint)
			SELECT $1::uuid, url, $2
			FROM unnest($3::text[]) AS urls(url)
			ON CONFLICT DO NOTHING`,
			input.MessageID, fingerprint, input.LinkScanURLs,
		); err != nil {
			return domain.Message{}, fmt.Errorf("record message link scans: %w", err)
		}
	}

	row := tx.QueryRow(ctx, `
		WITH updated AS (
			UPDATE chat.messages
			SET body_text = $2, body_format = $3, edited_at = $5,
			    edit_count = edit_count + 1, updated_at = $5,
			    link_safety_state = $6,
			    link_safety_fingerprint = NULLIF($7, ''),
			    link_safety_projection_version = link_safety_projection_version + 1
			WHERE id = $1
			RETURNING *
		)
		SELECT `+listMessageWithQuoteColumns("m", "$4", "q")+`
		FROM updated m
		LEFT JOIN auth.users u ON u.id = m.sender_id`+quotedMessageJoin("m", "q"),
		input.MessageID, input.Body, string(input.BodyFormat), input.EditorID, databaseNow,
		string(input.LinkSafetyState), fingerprint,
	)
	updated, err := scanMessageWithSenderAndQuote(row)
	if err != nil {
		return domain.Message{}, fmt.Errorf("update message body: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Message{}, fmt.Errorf("commit message edit: %w", err)
	}
	return updated, nil
}

// assertEditableLinkStates re-checks, inside the edit's transaction, that every
// URL in the new body is still in a state an edit may publish.
//
// It exists to close a time-of-check window. The service classifies the new body
// before opening this transaction, and a reconciliation running concurrently can
// change a verdict in between — most importantly from inconclusive to malicious.
// Without this the edit would publish a body carrying a URL that had just been
// condemned.
//
// The rule is the same one the classification applied, restated against the rows
// as they are now:
//
//   - a fresh malicious verdict refuses the edit outright;
//   - a URL with no terminal state means the edit must wait, because an edit
//     cannot be withheld the way a new message can;
//   - a fresh clearance and a terminal inconclusive both pass.
//
// The errors are the ones the caller already maps, so a race produces exactly the
// answer the non-racing path would have produced a moment earlier.
//
// Lock order is message first, then scan rows by canonical_url. Reconciliation
// commits its scan-row CAS before opening the message convergence transaction,
// so it never holds a scan lock while waiting for a message lock and cannot form
// the inverse scan -> message edge.
func assertEditableLinkStates(ctx context.Context, tx pgx.Tx, canonicalURLs []string) error {
	if len(canonicalURLs) == 0 {
		return nil
	}
	rows, err := tx.Query(ctx, `
		SELECT canonical_url, status
		FROM chat.link_scans
		WHERE canonical_url = ANY($1::text[])
		  AND ((status IN ('safe', 'malicious')
		        AND decided_at > now() - ($2 * interval '1 second'))
		       OR status = 'inconclusive')
		ORDER BY canonical_url
		FOR UPDATE`,
		canonicalURLs, urlsafety.VerdictTTL.Seconds(),
	)
	if err != nil {
		return fmt.Errorf("read link states for edit: %w", err)
	}
	defer rows.Close()

	decided := make(map[string]struct{}, len(canonicalURLs))
	for rows.Next() {
		var url, status string
		if err := rows.Scan(&url, &status); err != nil {
			return fmt.Errorf("scan link state for edit: %w", err)
		}
		if urlsafety.Verdict(status) == urlsafety.VerdictMalicious {
			return domain.ErrMaliciousURL
		}
		decided[url] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read link states for edit: %w", err)
	}
	for _, url := range canonicalURLs {
		if _, ok := decided[url]; !ok {
			return domain.ErrURLCheckPending
		}
	}
	return nil
}

// DeleteMessage atomically re-checks read access and authorship, then marks the
// row deleted. The bool reports whether this call performed the state change.
func (s *PGXMessageStore) DeleteMessage(ctx context.Context, input DeleteMessageInput) (domain.Message, bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.Message{}, false, fmt.Errorf("begin message delete: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var current domain.Message
	var deletedAt *time.Time
	var databaseNow time.Time
	err = tx.QueryRow(ctx, `
		SELECT m.sender_id::text, m.kind, m.status, m.deleted_at, clock_timestamp()
		FROM chat.messages m`+messageAccessJoins("$3")+`
		WHERE m.workspace_id = $1 AND m.id = $2
		  AND `+messageAccessPredicate("$3")+`
		FOR UPDATE OF m`,
		input.WorkspaceID, input.MessageID, input.RequesterID,
	).Scan(&current.SenderID, (*string)(&current.Kind), (*string)(&current.Status), &deletedAt, &databaseNow)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.Message{}, false, domain.ErrNotFound
		}
		return domain.Message{}, false, fmt.Errorf("lock deletable message: %w", err)
	}
	if deletedAt != nil {
		current.DeletedAt = *deletedAt
	}
	if err := domain.ValidateMessageDelete(current, input.RequesterID); err != nil {
		return domain.Message{}, false, err
	}

	changed := current.Status != domain.MessageStatusDeleted || deletedAt == nil
	if changed {
		tag, err := tx.Exec(ctx, `
			UPDATE chat.messages
			SET status = 'deleted', deleted_at = COALESCE(deleted_at, $4), updated_at = $4
			WHERE id = $1 AND workspace_id = $2 AND sender_id = $3`,
			input.MessageID, input.WorkspaceID, input.RequesterID, databaseNow,
		)
		if err != nil {
			return domain.Message{}, false, fmt.Errorf("soft delete message: %w", err)
		}
		if tag.RowsAffected() != 1 {
			return domain.Message{}, false, domain.ErrNotFound
		}
	}

	row := tx.QueryRow(ctx, `
		SELECT `+listMessageWithQuoteColumns("m", "$3", "q")+`
		FROM chat.messages m
		LEFT JOIN auth.users u ON u.id = m.sender_id`+quotedMessageJoin("m", "q")+`
		WHERE m.id = $1 AND m.workspace_id = $2`,
		input.MessageID, input.WorkspaceID, input.RequesterID,
	)
	deleted, err := scanMessageWithSenderAndQuote(row)
	if err != nil {
		return domain.Message{}, false, fmt.Errorf("read deleted message: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Message{}, false, fmt.Errorf("commit message delete: %w", err)
	}
	return deleted, changed, nil
}

func (s *PGXMessageStore) ListMessageEditHistory(ctx context.Context, input ListMessageEditHistoryInput) ([]domain.MessageEditHistory, error) {
	limit := resolveLimit(input.Limit)
	offset := max(input.Offset, 0)
	rows, err := s.pool.Query(ctx, `
		WITH authorized AS (
			SELECT m.id, m.link_safety_state
			FROM chat.messages m`+messageAccessJoins("$3")+`
			WHERE m.workspace_id = $1 AND m.id = $2
			  AND m.status = 'active' AND m.deleted_at IS NULL
			  AND `+messageAccessPredicate("$3")+`
		)
		SELECT COALESCE(h.id::text, ''), COALESCE(h.message_id::text, ''),
		       -- Every stored version, not only the one that was condemned. A
		       -- verdict is about a URL, and an edit that changed the wording
		       -- around it kept the URL — so the previous versions of a message
		       -- whose links are malicious are exactly where that URL is most
		       -- likely to still be written down. Withholding only the current
		       -- version would turn the history into the way to read it.
		       -- The version list itself is preserved: the reader still sees
		       -- that the message was edited and when, only not the text.
		       COALESCE(CASE
		         -- Pre-migration versions have no provable URL identity. Withhold
		         -- them instead of borrowing the current body's fingerprint.
		         WHEN h.link_safety_fingerprint IS NULL
		           OR a.link_safety_state = 'malicious' OR EXISTS (
		           SELECT 1
		           FROM chat.message_edit_history_link_scans hmls
		           LEFT JOIN chat.link_scans hls
		             ON hls.canonical_url = hmls.canonical_url
		           LEFT JOIN files.link_fetch_denylist deny
		             ON deny.url_digest = sha256(hmls.canonical_url::bytea)
		           WHERE hmls.history_id = h.id
		             AND (hls.status = 'malicious' OR deny.url_digest IS NOT NULL)
		         ) THEN '' ELSE h.body END, ''),
		       COALESCE(h.body_format, ''),
		       COALESCE(h.editor_user_id::text, ''), h.versioned_at
		FROM authorized a
		LEFT JOIN LATERAL (
			SELECT id, message_id, body, body_format, editor_user_id, versioned_at,
			       link_safety_fingerprint
			FROM chat.message_edit_history
			WHERE message_id = a.id
			ORDER BY versioned_at DESC, id DESC
			LIMIT $4 OFFSET $5
		) h ON true
		ORDER BY h.versioned_at DESC NULLS LAST, h.id DESC`,
		input.WorkspaceID, input.MessageID, input.UserID, limit, offset,
	)
	if err != nil {
		return nil, fmt.Errorf("list message edit history: %w", err)
	}
	defer rows.Close()

	found := false
	history := make([]domain.MessageEditHistory, 0, limit)
	for rows.Next() {
		found = true
		var version domain.MessageEditHistory
		var versionedAt *time.Time
		if err := rows.Scan(
			&version.ID, &version.MessageID, &version.Body, (*string)(&version.BodyFormat),
			&version.EditorUserID, &versionedAt,
		); err != nil {
			return nil, fmt.Errorf("scan message edit history: %w", err)
		}
		if version.ID != "" {
			version.VersionedAt = *versionedAt
			history = append(history, version)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate message edit history: %w", err)
	}
	if !found {
		return nil, domain.ErrNotFound
	}
	return history, nil
}

func (s *PGXMessageStore) ResolveMentionLabels(ctx context.Context, workspaceID string, userIDs, channelIDs []string) (map[string]string, error) {
	labels := make(map[string]string, len(userIDs)+len(channelIDs))
	if len(userIDs)+len(channelIDs) == 0 {
		return labels, nil
	}
	rows, err := s.pool.Query(ctx, `
		SELECT 'user', u.id::text, u.display_name
		FROM unnest($2::text[]) AS ids(id)
		JOIN auth.users u
		  ON u.id = ids.id::uuid AND u.status = 'active' AND u.deleted_at IS NULL
		JOIN chat.workspace_members wm
		  ON wm.user_id = u.id AND wm.workspace_id = $1::uuid AND wm.status = 'active'
		UNION ALL
		SELECT 'channel', c.id::text, c.display_name
		FROM unnest($3::text[]) AS ids(id)
		JOIN chat.channels c
		  ON c.id = ids.id::uuid AND c.workspace_id = $1::uuid AND c.status = 'active'`,
		workspaceID, userIDs, channelIDs,
	)
	if err != nil {
		return nil, fmt.Errorf("resolve mention labels: %w", err)
	}
	defer rows.Close()
	return scanMentionLabels(rows, labels)
}

func (s *PGXMessageStore) ResolveAuthorizedMentionLabels(ctx context.Context, workspaceID, sourceChannelID, sourceDMConversationID, requesterID string, userIDs, channelIDs []string) (map[string]string, error) {
	labels := make(map[string]string, len(userIDs)+len(channelIDs))
	if len(userIDs)+len(channelIDs) == 0 {
		return labels, nil
	}
	rows, err := s.pool.Query(ctx, `
		SELECT 'user', u.id::text, u.display_name
		FROM unnest($5::text[]) AS ids(id)
		JOIN chat.channels source_channel
		  ON source_channel.id = $2::uuid
		 AND source_channel.workspace_id = $1::uuid
		 AND source_channel.status = 'active'
		JOIN chat.channel_members cm
		  ON cm.channel_id = source_channel.id AND cm.user_id = ids.id::uuid
		JOIN chat.workspace_members wm
		  ON wm.workspace_id = source_channel.workspace_id
		 AND wm.user_id = cm.user_id
		 AND wm.status = 'active'
		JOIN auth.users u
		  ON u.id = cm.user_id AND u.status = 'active' AND u.deleted_at IS NULL
		UNION ALL
		SELECT 'user', u.id::text, u.display_name
		FROM unnest($5::text[]) AS ids(id)
		JOIN chat.dm_conversations source_dm
		  ON source_dm.id = $3::uuid
		 AND source_dm.workspace_id = $1::uuid
		 AND source_dm.type = 'group'
		 AND source_dm.status = 'active'
		JOIN chat.dm_members dm
		  ON dm.conversation_id = source_dm.id AND dm.user_id = ids.id::uuid AND dm.status = 'active'
		JOIN chat.workspace_members wm
		  ON wm.workspace_id = source_dm.workspace_id
		 AND wm.user_id = dm.user_id
		 AND wm.status = 'active'
		JOIN auth.users u
		  ON u.id = dm.user_id AND u.status = 'active' AND u.deleted_at IS NULL
		UNION ALL
		SELECT 'channel', c.id::text, c.display_name
		FROM unnest($6::text[]) AS ids(id)
		JOIN chat.channels c
		  ON c.id = ids.id::uuid
		 AND c.workspace_id = $1::uuid
		 AND c.status = 'active'
		JOIN chat.workspaces w
		  ON w.id = c.workspace_id AND w.status = 'active'
		JOIN chat.workspace_members requester
		  ON requester.workspace_id = c.workspace_id
		 AND requester.user_id = $4::uuid
		 AND requester.status = 'active'
		WHERE $2::uuid IS NOT NULL
		  AND chat.channel_visible_to_user(c.id, $4::uuid)`,
		workspaceID, nullableUUID(sourceChannelID), nullableUUID(sourceDMConversationID), requesterID, userIDs, channelIDs,
	)
	if err != nil {
		return nil, fmt.Errorf("resolve authorized mention labels: %w", err)
	}
	defer rows.Close()
	return scanMentionLabels(rows, labels)
}

// CountEligibleAllMentionRecipientsUpTo mirrors CreateMessage's own
// eligible_all_mention_recipients predicate exactly, as a single set-based
// count — no row is ever fetched to Go, and no member profile is read.
//
// The result SATURATES at limit: the LIMIT sits inside the subquery that reads
// membership, below the aggregate, so the database stops after limit matching
// rows and never walks the rest of an arbitrarily large roster. A caller
// asking with limit = MaxGroupAllMentionRecipients+1 therefore learns
// "0..50 exactly" or "at least 51", which is all a bound decision needs, and
// the answer costs the same for a 51-member group and a 50,000-member one.
//
// Because it saturates it is never the group's real size and must not be
// rendered as one; it exists only to be compared against the bound.
//
// dmConversationID must name an active group DM or the count is 0, which the
// caller reads as "no recipients," not as an error: a 1:1 or missing target
// has no business calling this at all, and CreateMessage's own authorization
// is what actually decides accessibility.
func (s *PGXMessageStore) CountEligibleAllMentionRecipientsUpTo(ctx context.Context, workspaceID, dmConversationID string, limit int) (int, error) {
	if limit <= 0 {
		return 0, fmt.Errorf("count eligible all-mention recipients: %w: limit must be positive", domain.ErrInvalidInput)
	}
	var count int
	// No DISTINCT: chat.dm_members is keyed (conversation_id, user_id) and both
	// joins below are on primary keys, so one eligible member yields exactly one
	// row already. Dropping it is what lets LIMIT stop early instead of forcing
	// a dedupe over the whole roster first.
	err := s.pool.QueryRow(ctx, `
		SELECT count(*)
		FROM (
			SELECT 1
			FROM chat.dm_conversations dc
			JOIN chat.dm_members dm
			  ON dm.conversation_id = dc.id
			 AND dm.status = 'active'
			JOIN chat.workspace_members wm
			  ON wm.workspace_id = dc.workspace_id
			 AND wm.user_id = dm.user_id
			 AND wm.status = 'active'
			JOIN auth.users u
			  ON u.id = dm.user_id
			 AND u.status = 'active'
			 AND u.deleted_at IS NULL
			WHERE dc.id = $2::uuid
			  AND dc.workspace_id = $1::uuid
			  AND dc.type = 'group'
			  AND dc.status = 'active'
			LIMIT $3::int
		) eligible_limited`,
		workspaceID, dmConversationID, limit,
	).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count eligible all-mention recipients: %w", err)
	}
	return count, nil
}

func scanMentionLabels(rows pgx.Rows, labels map[string]string) (map[string]string, error) {
	for rows.Next() {
		var kind, id, label string
		if err := rows.Scan(&kind, &id, &label); err != nil {
			return nil, fmt.Errorf("scan mention label: %w", err)
		}
		labels[kind+":"+id] = label
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate mention labels: %w", err)
	}
	return labels, nil
}

func (s *PGXMessageStore) ValidateRefMessageInTarget(ctx context.Context, workspaceID, channelID, dmConversationID, messageID, userID string) error {
	var exists int
	err := s.pool.QueryRow(ctx, `
		SELECT 1
		FROM chat.messages m`+messageAccessJoins("$5")+`
		WHERE m.id = $1
		  AND m.workspace_id = $2
		  AND m.status = 'active'
		  -- Reply parents and forward provenance both presuppose something a
		  -- person said. A system message is an event, has no body to quote and
		  -- cannot be a thread root, so it is not a valid reference here
		  -- (issue #527). The cross-channel forward path already required this.
		  AND m.kind = 'user'
		  AND m.channel_id IS NOT DISTINCT FROM $3
		  AND m.dm_conversation_id IS NOT DISTINCT FROM $4
		  AND `+messageAccessPredicate("$5"),
		messageID, workspaceID, nullableUUID(channelID), nullableUUID(dmConversationID), userID,
	).Scan(&exists)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ErrInvalidMessageReference
		}
		return fmt.Errorf("validate ref message in target: %w", err)
	}
	return nil
}

func (s *PGXMessageStore) ResolveMessageReferences(ctx context.Context, workspaceID, userID string, messageIDs []string) (map[string]domain.MessageReference, error) {
	resolved := make(map[string]domain.MessageReference, len(messageIDs))
	if len(messageIDs) == 0 {
		return resolved, nil
	}

	rows, err := s.pool.Query(ctx, `
		SELECT m.id::text,
		       CASE WHEN m.channel_id IS NOT NULL THEN 'channel' ELSE 'dm' END,
		       COALESCE(m.channel_id::text, m.dm_conversation_id::text),
		       COALESCE(c.display_name, dc.title, ''),
		       COALESCE(author.display_name, ''),
		       `+withheldIfMalicious("m.body_text", "m.link_safety_state")+`, m.body_format, m.created_at,
		       m.updated_at,
		       COALESCE(m.link_safety_state, '')
		FROM unnest($3::text[]) AS ids(id)
		JOIN chat.messages m ON m.id = ids.id::uuid`+messageAccessJoins("$2")+`
		LEFT JOIN auth.users author ON author.id = m.sender_id
		WHERE m.workspace_id = $1::uuid
		  AND m.status = 'active'
		  AND m.deleted_at IS NULL
		  -- A quote shows what somebody wrote. A system message has no body to
		  -- show, so quoting one would render an empty preview; it is simply not
		  -- a referenceable message (issue #527).
		  AND m.kind = 'user'
		  AND `+messageAccessPredicate("$2"),
		workspaceID, userID, messageIDs,
	)
	if err != nil {
		return nil, fmt.Errorf("resolve message references: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var ref domain.MessageReference
		ref.Available = true
		if err := rows.Scan(
			&ref.MessageID, &ref.TargetType, &ref.TargetID, &ref.TargetLabel,
			&ref.AuthorDisplayName, &ref.BodyText, (*string)(&ref.BodyFormat), &ref.CreatedAt, &ref.UpdatedAt,
			(*string)(&ref.LinkSafety),
		); err != nil {
			return nil, fmt.Errorf("scan message reference: %w", err)
		}
		resolved[ref.MessageID] = ref
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate message references: %w", err)
	}
	return resolved, nil
}

func (s *PGXMessageStore) ListReferencedMessageIDs(ctx context.Context, workspaceID, userID, channelID, dmConversationID string, messageIDs []string) (map[string]string, error) {
	referenced := make(map[string]string, len(messageIDs))
	if len(messageIDs) == 0 {
		return referenced, nil
	}

	rows, err := s.pool.Query(ctx, `
		SELECT m.id::text, m.referenced_message_id::text
		FROM unnest($5::text[]) AS ids(id)
		JOIN chat.messages m ON m.id = ids.id::uuid`+messageAccessJoins("$2")+`
		WHERE m.workspace_id = $1::uuid
		  AND m.channel_id IS NOT DISTINCT FROM $3::uuid
		  AND m.dm_conversation_id IS NOT DISTINCT FROM $4::uuid
		  AND m.status = 'active'
		  AND m.deleted_at IS NULL
		  AND m.referenced_message_id IS NOT NULL
		  AND `+messageAccessPredicate("$2"),
		workspaceID, userID, nullableUUID(channelID), nullableUUID(dmConversationID), messageIDs,
	)
	if err != nil {
		return nil, fmt.Errorf("list referenced message ids: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var destinationID, sourceID string
		if err := rows.Scan(&destinationID, &sourceID); err != nil {
			return nil, fmt.Errorf("scan referenced message id: %w", err)
		}
		referenced[destinationID] = sourceID
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate referenced message ids: %w", err)
	}
	return referenced, nil
}

func (s *PGXMessageStore) GetMessageByIDInWorkspace(ctx context.Context, workspaceID, messageID, userID string) (domain.Message, error) {
	// Use listMessageWithQuoteColumns so the result matches the list endpoint
	// contract: sender display info, favorite flag, and optional quote preview.
	row := s.pool.QueryRow(ctx, `
		SELECT `+listMessageWithQuoteColumns("m", "$3", "q")+`
		FROM chat.messages m`+messageAccessJoins("$3")+`
		LEFT JOIN auth.users u ON u.id = m.sender_id`+quotedMessageJoin("m", "q")+`
		WHERE m.id = $1 AND m.workspace_id = $2
		  AND `+messageAccessPredicate("$3")+`
		  AND `+messageVisibilityPredicate("m", "$3"),
		messageID, workspaceID, userID,
	)
	msg, err := scanMessageWithSenderAndQuote(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.Message{}, domain.ErrNotFound
		}
		return domain.Message{}, fmt.Errorf("get message by id in workspace: %w", err)
	}
	messages := []domain.Message{msg}
	if err := s.loadReactionBatch(ctx, messages, userID); err != nil {
		return domain.Message{}, err
	}
	if err := s.loadAttachmentBatch(ctx, messages); err != nil {
		return domain.Message{}, err
	}
	return messages[0], nil
}

func (s *PGXMessageStore) ListMessageSecuritySnapshots(
	ctx context.Context, workspaceID, userID, channelID, dmConversationID string, messageIDs []string,
) ([]MessageSecuritySnapshot, error) {
	rows, err := s.pool.Query(ctx, `
		WITH requested AS (
			SELECT id::uuid, ordinality
			FROM unnest($5::text[]) WITH ORDINALITY AS ids(id, ordinality)
		), visible AS (
			SELECT m.id, m.status, m.link_safety_state, m.updated_at, m.parent_message_id
			FROM chat.messages m`+messageAccessJoins("$2")+`
			WHERE m.workspace_id = $1::uuid
			  AND m.channel_id IS NOT DISTINCT FROM $3::uuid
			  AND m.dm_conversation_id IS NOT DISTINCT FROM $4::uuid
			  AND `+messageAccessPredicate("$2")+`
			  AND `+messageVisibilityPredicate("m", "$2")+`
		)
		SELECT requested.id::text, visible.id IS NOT NULL,
		       COALESCE(visible.status, ''), COALESCE(visible.link_safety_state, ''),
		       COALESCE(visible.updated_at, to_timestamp(0)),
		       COALESCE(q.id::text, ''), COALESCE(q.status, ''),
		       COALESCE(q.link_safety_state, ''), COALESCE(q.updated_at, to_timestamp(0))
		FROM requested
		LEFT JOIN visible ON visible.id = requested.id
		LEFT JOIN chat.messages q
		  ON q.id = visible.parent_message_id
		 AND q.workspace_id = $1::uuid
		 AND q.channel_id IS NOT DISTINCT FROM $3::uuid
		 AND q.dm_conversation_id IS NOT DISTINCT FROM $4::uuid
		ORDER BY requested.ordinality`,
		workspaceID, userID, nullableUUID(channelID), nullableUUID(dmConversationID), messageIDs,
	)
	if err != nil {
		return nil, fmt.Errorf("list message security snapshots: %w", err)
	}
	defer rows.Close()

	snapshots := make([]MessageSecuritySnapshot, 0, len(messageIDs))
	for rows.Next() {
		var snapshot MessageSecuritySnapshot
		var status, linkSafety, quoteID, quoteStatus, quoteLinkSafety string
		var quoteUpdatedAt time.Time
		if err := rows.Scan(&snapshot.MessageID, &snapshot.Available, &status, &linkSafety,
			&snapshot.UpdatedAt, &quoteID, &quoteStatus, &quoteLinkSafety, &quoteUpdatedAt); err != nil {
			return nil, fmt.Errorf("scan message security snapshot: %w", err)
		}
		if snapshot.Available {
			snapshot.Status = domain.MessageStatus(status)
			snapshot.LinkSafetyState = domain.MessageLinkSafety(linkSafety)
			if quoteID != "" {
				snapshot.Quoted = &QuotedMessageSecuritySnapshot{
					MessageID: quoteID, Status: domain.MessageStatus(quoteStatus),
					LinkSafetyState: domain.MessageLinkSafety(quoteLinkSafety), UpdatedAt: quoteUpdatedAt,
				}
			}
		}
		snapshots = append(snapshots, snapshot)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate message security snapshots: %w", err)
	}
	return snapshots, nil
}

func (s *PGXMessageStore) ListChannelMessages(ctx context.Context, input ListChannelMessagesInput) (ListMessagesResult, error) {
	limit := resolveLimit(input.Limit)

	var rows pgx.Rows
	var err error

	if input.BeforeCursor != nil {
		// Keyset pagination: fetch messages older than the cursor.
		// Fetch limit+1 to detect whether a next page exists.
		rows, err = s.pool.Query(ctx, `
			SELECT `+listMessageWithQuoteColumns("m", "$3", "q")+`
			FROM chat.messages m
			JOIN chat.channels c
			  ON c.id = m.channel_id
			JOIN chat.workspaces w
			  ON w.id = m.workspace_id AND w.status = 'active'
			JOIN chat.workspace_members wm
			  ON wm.workspace_id = m.workspace_id AND wm.user_id = $3 AND wm.status = 'active'
			LEFT JOIN auth.users u
			  ON u.id = m.sender_id`+quotedMessageJoin("m", "q")+`
			WHERE m.workspace_id = $1
			  AND m.channel_id = $2
			  AND c.status = 'active'
			  AND chat.channel_visible_to_user(c.id, $3::uuid)
			  AND `+messageVisibilityPredicate("m", "$3")+`
			  AND (m.created_at, m.id) < ($4, $5::uuid)
			ORDER BY m.created_at DESC, m.id DESC
			LIMIT $6`,
			input.WorkspaceID, input.ChannelID, input.UserID,
			input.BeforeCursor.CreatedAt, input.BeforeCursor.ID,
			limit+1,
		)
	} else {
		rows, err = s.pool.Query(ctx, `
			SELECT `+listMessageWithQuoteColumns("m", "$3", "q")+`
			FROM chat.messages m
			JOIN chat.channels c
			  ON c.id = m.channel_id
			JOIN chat.workspaces w
			  ON w.id = m.workspace_id AND w.status = 'active'
			JOIN chat.workspace_members wm
			  ON wm.workspace_id = m.workspace_id AND wm.user_id = $3 AND wm.status = 'active'
			LEFT JOIN auth.users u
			  ON u.id = m.sender_id`+quotedMessageJoin("m", "q")+`
			WHERE m.workspace_id = $1
			  AND m.channel_id = $2
			  AND c.status = 'active'
			  AND chat.channel_visible_to_user(c.id, $3::uuid)
			  AND `+messageVisibilityPredicate("m", "$3")+`
			ORDER BY m.created_at DESC, m.id DESC
			LIMIT $4`,
			input.WorkspaceID, input.ChannelID, input.UserID,
			limit+1,
		)
	}
	if err != nil {
		return ListMessagesResult{}, fmt.Errorf("list channel messages: %w", err)
	}
	defer rows.Close()
	result, err := collectListMessagesResult(rows, limit)
	rows.Close()
	if err != nil {
		return ListMessagesResult{}, err
	}
	if err := s.loadReactionBatch(ctx, result.Messages, input.UserID); err != nil {
		return ListMessagesResult{}, err
	}
	if err := s.loadAttachmentBatch(ctx, result.Messages); err != nil {
		return ListMessagesResult{}, err
	}
	return result, nil
}

func (s *PGXMessageStore) ListDMMessages(ctx context.Context, input ListDMMessagesInput) (ListMessagesResult, error) {
	limit := resolveLimit(input.Limit)

	var rows pgx.Rows
	var err error

	if input.BeforeCursor != nil {
		rows, err = s.pool.Query(ctx, `
			SELECT `+listMessageWithQuoteColumns("m", "$3", "q")+`
			FROM chat.messages m
			JOIN chat.dm_conversations dc
			  ON dc.id = m.dm_conversation_id
			JOIN chat.workspaces w
			  ON w.id = m.workspace_id AND w.status = 'active'
			JOIN chat.workspace_members wm
			  ON wm.workspace_id = m.workspace_id AND wm.user_id = $3 AND wm.status = 'active'
			JOIN chat.dm_members dm
			  ON dm.conversation_id = m.dm_conversation_id AND dm.user_id = $3 AND dm.status = 'active'
			LEFT JOIN auth.users u
			  ON u.id = m.sender_id`+quotedMessageJoin("m", "q")+`
			WHERE m.workspace_id = $1
			  AND m.dm_conversation_id = $2
			  AND dc.status = 'active'
			  AND `+messageVisibilityPredicate("m", "$3")+`
			  AND (m.created_at, m.id) < ($4, $5::uuid)
			ORDER BY m.created_at DESC, m.id DESC
			LIMIT $6`,
			input.WorkspaceID, input.ConversationID, input.UserID,
			input.BeforeCursor.CreatedAt, input.BeforeCursor.ID,
			limit+1,
		)
	} else {
		rows, err = s.pool.Query(ctx, `
			SELECT `+listMessageWithQuoteColumns("m", "$3", "q")+`
			FROM chat.messages m
			JOIN chat.dm_conversations dc
			  ON dc.id = m.dm_conversation_id
			JOIN chat.workspaces w
			  ON w.id = m.workspace_id AND w.status = 'active'
			JOIN chat.workspace_members wm
			  ON wm.workspace_id = m.workspace_id AND wm.user_id = $3 AND wm.status = 'active'
			JOIN chat.dm_members dm
			  ON dm.conversation_id = m.dm_conversation_id AND dm.user_id = $3 AND dm.status = 'active'
			LEFT JOIN auth.users u
			  ON u.id = m.sender_id`+quotedMessageJoin("m", "q")+`
			WHERE m.workspace_id = $1
			  AND m.dm_conversation_id = $2
			  AND dc.status = 'active'
			  AND `+messageVisibilityPredicate("m", "$3")+`
			ORDER BY m.created_at DESC, m.id DESC
			LIMIT $4`,
			input.WorkspaceID, input.ConversationID, input.UserID,
			limit+1,
		)
	}
	if err != nil {
		return ListMessagesResult{}, fmt.Errorf("list dm messages: %w", err)
	}
	defer rows.Close()
	result, err := collectListMessagesResult(rows, limit)
	rows.Close()
	if err != nil {
		return ListMessagesResult{}, err
	}
	if err := s.loadReactionBatch(ctx, result.Messages, input.UserID); err != nil {
		return ListMessagesResult{}, err
	}
	if err := s.loadAttachmentBatch(ctx, result.Messages); err != nil {
		return ListMessagesResult{}, err
	}
	return result, nil
}

// reactionAuthorLimit is how many names travel with each reaction aggregate.
//
// The tooltip renders at most two entries and summarises the rest from the
// count, and one of those two may be "Você" — so three is the smallest prefix
// that always fills the tooltip whoever the viewer is. Sending the whole set
// would be payload nobody renders, and would grow without bound on a popular
// message.
const reactionAuthorLimit = 3

func (s *PGXMessageStore) loadReactionBatch(ctx context.Context, messages []domain.Message, userID string) error {
	if len(messages) == 0 {
		return nil
	}
	ids := make([]string, len(messages))
	byID := make(map[string]int, len(messages))
	for i := range messages {
		ids[i] = messages[i].ID
		byID[messages[i].ID] = i
		messages[i].Reactions = []domain.MessageReaction{}
	}
	rows, err := s.pool.Query(ctx, reactionBatchQuery, ids, userID, reactionAuthorLimit)
	if err != nil {
		return fmt.Errorf("load message reactions: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		messageID, reaction, err := scanMessageReaction(rows)
		if err != nil {
			return err
		}
		if i, ok := byID[messageID]; ok {
			messages[i].Reactions = append(messages[i].Reactions, reaction)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate message reactions: %w", err)
	}
	return nil
}

// reactionBatchQuery aggregates a whole page of messages in one statement
// (never one per message, and never one per reacting user): counts, whether the
// reader is among them, and the first few display names behind each emoji.
//
// Authorization is the caller's: this runs only over message IDs the outer list
// query already resolved for this reader, so it can add no reach of its own. A
// user with no display name is left out of the names but still counted, which
// is what keeps "e mais N" arithmetic honest without inventing a label.
const reactionBatchQuery = `
	WITH scoped AS (
		SELECT message_id, emoji, user_id, created_at
		FROM chat.message_reactions
		WHERE message_id = ANY($1::uuid[])
	),
	totals AS (
		SELECT message_id, emoji, count(*)::int AS count,
		       bool_or(user_id = $2) AS reacted_by_me, min(created_at) AS first_created_at
		FROM scoped
		GROUP BY message_id, emoji
	),
	named AS (
		SELECT s.message_id, s.emoji, s.user_id, u.display_name,
		       row_number() OVER (PARTITION BY s.message_id, s.emoji
		                          ORDER BY s.created_at, s.user_id) AS seq
		FROM scoped s
		JOIN auth.users u ON u.id = s.user_id
		WHERE u.display_name <> ''
	),
	authors AS (
		SELECT message_id, emoji,
		       array_agg(user_id::text ORDER BY seq) AS user_ids,
		       array_agg(display_name ORDER BY seq) AS display_names
		FROM named
		WHERE seq <= $3
		GROUP BY message_id, emoji
	)
	SELECT t.message_id::text, t.emoji, t.count, t.reacted_by_me,
	       COALESCE(a.user_ids, '{}'), COALESCE(a.display_names, '{}')
	FROM totals t
	LEFT JOIN authors a ON a.message_id = t.message_id AND a.emoji = t.emoji
	ORDER BY t.message_id, t.first_created_at, t.emoji`

func scanMessageReaction(rows pgx.Rows) (string, domain.MessageReaction, error) {
	var messageID string
	var reaction domain.MessageReaction
	var userIDs, displayNames []string
	if err := rows.Scan(
		&messageID, &reaction.Emoji, &reaction.Count, &reaction.ReactedByMe, &userIDs, &displayNames,
	); err != nil {
		return "", domain.MessageReaction{}, fmt.Errorf("scan message reaction: %w", err)
	}
	reaction.Users = zipReactionUsers(userIDs, displayNames)
	return messageID, reaction, nil
}

// zipReactionUsers pairs the two parallel arrays the aggregate returns. They are
// produced by one array_agg pass each over the same ordered rows, so a length
// mismatch is impossible in practice; the shorter bound is taken anyway so a
// surprise truncates instead of panicking mid-page.
func zipReactionUsers(userIDs, displayNames []string) []domain.ReactionUser {
	size := min(len(userIDs), len(displayNames))
	if size == 0 {
		return nil
	}
	users := make([]domain.ReactionUser, size)
	for i := range size {
		users[i] = domain.ReactionUser{UserID: userIDs[i], DisplayName: displayNames[i]}
	}
	return users
}

// loadAttachmentBatch fills Attachments for a whole page in one query (RF-32).
//
// It is deliberately shaped exactly like loadReactionBatch: one statement for
// every message on the page, keyed by the chat.message_attachments primary key,
// so a page of 50 messages costs one query and not 50. The join into
// files.attachments is a read of another service's table — the same direction
// file-service already reads chat.channels and chat.workspaces — and it selects
// only the columns a viewer may see. Storage keys, key material and scanner
// detail are not in the projection at all, so they cannot leak by being carried
// into a struct and forgotten.
//
// Soft-deleted attachments are filtered out rather than rendered as a broken
// row: a removed file stops being listed everywhere else too.
func (s *PGXMessageStore) loadAttachmentBatch(ctx context.Context, messages []domain.Message) error {
	if len(messages) == 0 {
		return nil
	}
	ids := make([]string, len(messages))
	byID := make(map[string]int, len(messages))
	for i := range messages {
		ids[i] = messages[i].ID
		byID[messages[i].ID] = i
	}
	rows, err := s.pool.Query(ctx, `
		SELECT ma.message_id::text, a.id::text, a.original_filename,
		       COALESCE(NULLIF(a.detected_mime, ''), a.declared_mime),
		       a.size_bytes, a.status, a.preview_status,
		       COALESCE(a.audio_kind, ''), COALESCE(a.declared_duration_ms, 0)
		FROM chat.message_attachments ma
		JOIN files.attachments a ON a.id = ma.attachment_id
		WHERE ma.message_id = ANY($1::uuid[])
		  AND a.deleted_at IS NULL
		ORDER BY ma.message_id, ma.position, a.id`, ids)
	if err != nil {
		return fmt.Errorf("load message attachments: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var messageID string
		var attachment domain.MessageAttachment
		if err := rows.Scan(
			&messageID, &attachment.ID, &attachment.Filename, &attachment.ContentType,
			&attachment.SizeBytes, &attachment.Status, &attachment.PreviewStatus,
			&attachment.AudioKind, &attachment.DurationMs,
		); err != nil {
			return fmt.Errorf("scan message attachment: %w", err)
		}
		if i, ok := byID[messageID]; ok {
			messages[i].Attachments = append(messages[i].Attachments, attachment)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate message attachments: %w", err)
	}
	return nil
}

// resolveLimit returns a valid limit value: defaults to defaultMessageLimit when
// input is 0, capped at maxMessageLimit.
func resolveLimit(n int) int {
	if n <= 0 {
		return defaultMessageLimit
	}
	if n > maxMessageLimit {
		return maxMessageLimit
	}
	return n
}

type messageRows interface {
	Next() bool
	Scan(dest ...any) error
	Err() error
}

func collectListMessagesResult(rows messageRows, limit int) (ListMessagesResult, error) {
	return doCollectMessagesResult(rows, limit, true)
}

func doCollectMessagesResult(rows messageRows, limit int, withSender bool) (ListMessagesResult, error) {
	msgs, err := collectMessagesWithSenderAndQuote(rows, withSender)
	if err != nil {
		return ListMessagesResult{}, err
	}

	var nextCursor *MessageCursor
	if len(msgs) > limit {
		// Extra row means there is an older page. Trim to limit.
		msgs = msgs[:limit]
		// The last element (after trimming, still DESC) is the oldest we're returning.
		oldest := msgs[len(msgs)-1]
		c := MessageCursor{CreatedAt: oldest.CreatedAt, ID: oldest.ID}
		nextCursor = &c
	}

	// Reverse from DESC to ASC (oldest-first) for display.
	for i, j := 0, len(msgs)-1; i < j; i, j = i+1, j-1 {
		msgs[i], msgs[j] = msgs[j], msgs[i]
	}

	return ListMessagesResult{Messages: msgs, NextCursor: nextCursor}, nil
}

func collectMessagesWithSenderAndQuote(rows messageRows, withSender bool) ([]domain.Message, error) {
	var messages []domain.Message
	for rows.Next() {
		var msg domain.Message
		var editedAt, deletedAt *time.Time
		var quote domain.QuotedMessage
		var quoteDeletedAt, quoteCreatedAt, quoteUpdatedAt *time.Time
		var eventPayload []byte
		dest := []any{
			&msg.ID, &msg.WorkspaceID,
			&msg.ChannelID, &msg.DMConversationID,
			&msg.SenderID,
			(*string)(&msg.Kind), &msg.BodyText, (*string)(&msg.BodyFormat), (*string)(&msg.Status),
			&msg.ParentMessageID, &msg.ForwardedFromMessageID, &msg.ReferencedMessageID,
			&editedAt, &msg.EditCount, &deletedAt,
			&msg.CreatedAt, &msg.UpdatedAt,
			(*string)(&msg.LinkSafety),
			&msg.EventType, &eventPayload,
		}
		if withSender {
			dest = append(dest, &msg.SenderDisplayName, &msg.SenderEmail, &msg.SenderAvatarURL, &msg.IsFavorited)
			dest = append(dest,
				&quote.ID, &quote.AuthorID, &quote.BodyText, (*string)(&quote.BodyFormat), (*string)(&quote.Status),
				&quoteDeletedAt, &quoteCreatedAt, &quoteUpdatedAt, (*string)(&quote.LinkSafety),
			)
		}
		err := rows.Scan(dest...)
		if err != nil {
			return nil, fmt.Errorf("scan message row: %w", err)
		}
		if err := decodeConversationEvent(&msg, eventPayload); err != nil {
			return nil, err
		}
		if editedAt != nil {
			msg.EditedAt = *editedAt
		}
		if deletedAt != nil {
			msg.DeletedAt = *deletedAt
		}
		if quote.ID != "" {
			if quoteDeletedAt != nil {
				quote.DeletedAt = *quoteDeletedAt
			}
			if quoteCreatedAt != nil {
				quote.CreatedAt = *quoteCreatedAt
			}
			if quoteUpdatedAt != nil {
				quote.UpdatedAt = *quoteUpdatedAt
			}
			msg.Quoted = &quote
		}
		messages = append(messages, msg)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate message rows: %w", err)
	}
	return messages, nil
}
