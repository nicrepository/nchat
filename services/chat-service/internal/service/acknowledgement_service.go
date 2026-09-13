package service

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

// AcknowledgementActionInput identifies the acknowledgement a caller is acting
// on (issue #824).
//
// ActorUserID is the authenticated principal, always, exactly as PinActionInput
// says of its own. There is deliberately no recipient field: the only person a
// caller may answer for is themselves, so "acknowledge on behalf of" is not an
// input this type can carry and therefore not a request the handler can be
// tricked into forwarding.
type AcknowledgementActionInput struct {
	WorkspaceID string
	MessageID   string
	ActorUserID string
}

// MaxAcknowledgementBatchSize bounds one page's worth of acknowledgement
// summaries (issue #824).
//
// The same ceiling as the reference and security-snapshot batches, and for the
// same reason: this is a client asking about the ids on one screen, and a
// message page is capped at 100 by resolveLimit. A caller cannot turn a page
// load into an arbitrarily large aggregation.
const MaxAcknowledgementBatchSize = 100

// ReadAcknowledgementBatchInput asks about the messages one timeline page is
// showing. ViewerID is the authenticated principal, exactly as everywhere else
// in this service.
type ReadAcknowledgementBatchInput struct {
	WorkspaceID string
	MessageIDs  []string
	ViewerID    string
}

// AcknowledgementService implements issue #824: explicit, per-recipient
// confirmation of receipt.
//
// It is a thin layer on purpose. The rules that matter — who may act, which
// transitions are legal, and what happens when two of them arrive at once —
// are decided by a single conditional UPDATE in the store, because a rule
// enforced in Go above a database that would accept the alternative is a rule
// that holds only while nothing races it.
type AcknowledgementService struct {
	acknowledgements storage.AcknowledgementStore
}

// NewAcknowledgementService returns an AcknowledgementService backed by the
// given store.
func NewAcknowledgementService(acknowledgements storage.AcknowledgementStore) *AcknowledgementService {
	return &AcknowledgementService{acknowledgements: acknowledgements}
}

// AcknowledgeOutcome is what an acknowledgement did: the summary the caller is
// shown, and the two facts a caller that announces the change needs — which
// conversation to announce it to, and whether anything actually changed.
type AcknowledgeOutcome = storage.AcknowledgeResult

// Acknowledge records the caller's own confirmation and returns the message's
// acknowledgement as the caller may see it.
//
// Idempotent: a retry, a double click and two concurrent requests converge on
// one row in one state. A caller whose request was already resolved — they
// replied, or the sender withdrew the message — is told that state rather than
// refused, because the client's job is to render what actually holds and a
// 409 it has to translate back into a refetch teaches it nothing extra.
//
// Returns ErrNotFound when the message is unreadable, absent, or never asked
// this caller anything — one answer, so the endpoint cannot be used to discover
// which messages exist or who their recipients are.
func (s *AcknowledgementService) Acknowledge(
	ctx context.Context, input AcknowledgementActionInput,
) (AcknowledgeOutcome, error) {
	input, err := validateAcknowledgementAction(input)
	if err != nil {
		return AcknowledgeOutcome{}, err
	}
	// Identifiers only. The message body, the recipient list and who has not
	// answered are all absent deliberately: this line is written on every
	// confirmation of every urgent message, and an audit trail is not a place to
	// accumulate the contents of private conversations.
	slog.InfoContext(ctx, "chat acknowledge message",
		"actor_user_id", input.ActorUserID,
		"message_id", input.MessageID,
	)
	return s.acknowledgements.Acknowledge(ctx, storage.AcknowledgeInput{
		WorkspaceID: input.WorkspaceID,
		MessageID:   input.MessageID,
		RecipientID: input.ActorUserID,
	})
}

// Read returns the acknowledgement summary for one message: the counts, the
// caller's own state, and — for the message's sender — the per-recipient
// detail. Reading is never acknowledging; nothing here writes.
func (s *AcknowledgementService) Read(
	ctx context.Context, input AcknowledgementActionInput,
) (domain.AcknowledgementSummary, error) {
	input, err := validateAcknowledgementAction(input)
	if err != nil {
		return domain.AcknowledgementSummary{}, err
	}
	return s.acknowledgements.ReadAcknowledgement(ctx, storage.ReadAcknowledgementInput{
		WorkspaceID: input.WorkspaceID,
		MessageID:   input.MessageID,
		ViewerID:    input.ActorUserID,
	})
}

// ReadBatch returns the acknowledgement summaries for a page of messages, keyed
// by message id (issue #824).
//
// One request and one statement for a whole page. The map holds an entry only
// for a message this caller may read and that the server had something to say
// about; everything else — absent, invisible, another tenant's — is simply not
// in it, which is the same non-enumerating answer the single read gives.
//
// It carries no per-recipient detail. A sender who wants to know who has not
// answered opens that one message, and the single read decides then whether they
// may see the list.
func (s *AcknowledgementService) ReadBatch(
	ctx context.Context, input ReadAcknowledgementBatchInput,
) (map[string]domain.AcknowledgementSummary, error) {
	workspaceID := strings.TrimSpace(input.WorkspaceID)
	viewerID := strings.TrimSpace(input.ViewerID)
	if workspaceID == "" || viewerID == "" {
		return nil, fmt.Errorf("%w: workspace and viewer are required", domain.ErrInvalidInput)
	}
	// The shared batch rule: bounded, every id a real UUID before any query
	// runs, and duplicates collapsed so repeating one id cannot multiply the
	// work. An empty list is a client bug rather than an empty answer, on the
	// same terms as every other batch endpoint here.
	messageIDs, err := normalizeMessageIDBatch(input.MessageIDs, MaxAcknowledgementBatchSize)
	if err != nil {
		return nil, err
	}
	return s.acknowledgements.ReadAcknowledgementBatch(ctx, storage.ReadAcknowledgementBatchInput{
		WorkspaceID: workspaceID,
		MessageIDs:  messageIDs,
		ViewerID:    viewerID,
	})
}

func validateAcknowledgementAction(input AcknowledgementActionInput) (AcknowledgementActionInput, error) {
	input.WorkspaceID = strings.TrimSpace(input.WorkspaceID)
	input.MessageID = strings.TrimSpace(input.MessageID)
	input.ActorUserID = strings.TrimSpace(input.ActorUserID)
	if input.WorkspaceID == "" || input.MessageID == "" || input.ActorUserID == "" {
		return input, fmt.Errorf("%w: workspace, message and actor are required", domain.ErrInvalidInput)
	}
	return input, nil
}
