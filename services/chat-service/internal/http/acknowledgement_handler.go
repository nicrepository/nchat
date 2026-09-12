package httpapi

import (
	"context"
	"net/http"
	"time"

	"github.com/nicrepository/nchat/libs/go/platform/httputil"
	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/service"
)

// acknowledgementProvider is the AcknowledgementService interface used by
// MessageHandler (issue #824).
type acknowledgementProvider interface {
	Acknowledge(ctx context.Context, in service.AcknowledgementActionInput) (service.AcknowledgeOutcome, error)
	Read(ctx context.Context, in service.AcknowledgementActionInput) (domain.AcknowledgementSummary, error)
	ReadBatch(
		ctx context.Context, in service.ReadAcknowledgementBatchInput,
	) (map[string]domain.AcknowledgementSummary, error)
}

// acknowledgementBroadcaster tells a conversation's subscribers that one of its
// messages' acknowledgement changed (issue #824).
//
// The signature carries a route and a message id and nothing else, which is the
// whole contract: the event is an invalidation hint, and what a given reader may
// then learn about the acknowledgement is decided by the authorised read, not by
// what was broadcast.
type acknowledgementBroadcaster interface {
	PublishAcknowledgementUpdated(ctx context.Context, workspaceID, targetType, targetID, messageID string)
}

// ── JSON response shapes ──────────────────────────────────────────────────────

// acknowledgementJSON is how one message's acknowledgement is reported to a
// client (issue #824).
//
// Every terminal state gets its own count rather than a confirmed/outstanding
// pair. "4 of 7 confirmed" is the sentence the product asks for, but a sender
// looking at the other three is owed the difference between somebody who
// replied, somebody whose request was withdrawn and somebody who simply has not
// answered — and a client that has to infer those from a subtraction will infer
// them differently in each place it renders them.
type acknowledgementJSON struct {
	MessageID string `json:"message_id"`
	// Required mirrors the message's own flag. A message that asked for nothing
	// answers here with all counts zero, which a client must be able to tell
	// from a message whose recipients have not answered yet.
	Required     bool `json:"required"`
	Total        int  `json:"total"`
	Pending      int  `json:"pending"`
	Acknowledged int  `json:"acknowledged"`
	Responded    int  `json:"responded"`
	Expired      int  `json:"expired"`
	Cancelled    int  `json:"cancelled"`
	// ViewerState is the caller's own state, or empty when this message never
	// asked them anything — its sender, or somebody who arrived afterwards.
	// Empty is not one of the states; it is the absence of a row.
	ViewerState string `json:"viewer_state"`
	// Recipients is present only for the message's sender. Everyone else who can
	// read the message gets the counts and their own state: which named
	// colleague has not answered yet is the sender's information, and a group's
	// members are not entitled to audit each other through it.
	Recipients []acknowledgementRecipientJSON `json:"recipients,omitempty"`
}

// acknowledgementRecipientJSON carries an id, a state and an instant, and no
// display name, address or avatar. The one viewer who receives this list is the
// sender, who can already resolve their conversation's members through the
// endpoints that exist for it; re-deriving those names here would add a second
// path personal data leaves this service by, for nothing the caller does not
// already have.
type acknowledgementRecipientJSON struct {
	RecipientID string `json:"recipient_id"`
	State       string `json:"state"`
	// ResolvedAt is when this recipient stopped being pending, null while they
	// still are. A pointer rather than a zero time so "not yet" is expressed as
	// absence instead of as the Unix epoch.
	ResolvedAt *time.Time `json:"resolved_at"`
}

// acknowledgementBatchRequest asks about the messages one timeline page holds.
//
// POST for a read, exactly like the link-safety status batch beside it: the
// request carries a list of ids, and a list does not belong in a query string.
type acknowledgementBatchRequest struct {
	MessageIDs []string `json:"message_ids"`
}

// acknowledgementBatchResponseData is keyed by message id so a client can map
// each answer onto the message it is drawing without scanning a list.
//
// A message the caller may not read simply has no key, which is the same
// non-enumerating answer the single read gives — there is no "forbidden" entry
// to tell one refusal from another.
type acknowledgementBatchResponseData struct {
	Acknowledgements map[string]acknowledgementJSON `json:"acknowledgements"`
}

// ── Shared helpers ────────────────────────────────────────────────────────────

func (h *MessageHandler) checkAcknowledgementDeps(w http.ResponseWriter) bool {
	if h.workspaces == nil || h.acknowledgements == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, "service_unavailable", "acknowledgements not available")
		return false
	}
	return true
}

// acknowledgementRequestContext validates the message path param and resolves
// the caller and workspace. Returns ok=false after writing the error response.
//
// ActorUserID comes from the authenticated session and from nowhere else. Both
// endpoints below are the reason this is stated once: an acknowledgement is
// only ever the caller's own, so there is no request shape in which a client
// names the recipient and consequently nothing for a future edit to forget to
// ignore.
func (h *MessageHandler) acknowledgementRequestContext(
	w http.ResponseWriter, r *http.Request,
) (in service.AcknowledgementActionInput, ok bool) {
	if !validateTargetID(w, r.PathValue("messageID"), "message_id") {
		return in, false
	}
	userID := GetContextUserID(r)
	if userID == "" {
		httputil.WriteError(w, http.StatusUnauthorized, httputil.ErrCodeUnauthorized, "unauthorized")
		return in, false
	}
	wsID, ok := h.resolveWorkspaceID(r.Context(), w)
	if !ok {
		return in, false
	}
	return service.AcknowledgementActionInput{
		WorkspaceID: wsID, MessageID: r.PathValue("messageID"), ActorUserID: userID,
	}, true
}

// ── Endpoints ─────────────────────────────────────────────────────────────────

// AcknowledgeMessage handles
// POST /api/chat/messages/{messageID}/acknowledgement.
//
// The caller confirms receipt of a message that asked them to. There is no
// request body at all — who is confirming is the session, and which message is
// the path — so there is nothing for a client to assert and nothing to
// mass-assign: a state, a timestamp and a recipient are all decided server-side.
//
// Idempotent, and answered with the resulting state rather than with 204: two
// clicks, a retry and two concurrent requests all produce the same single
// acknowledgement, and returning what actually holds is what lets a client that
// lost the first response stop guessing. A caller whose request was already
// resolved some other way is told so; 404 is reserved for a message that never
// asked them.
func (h *MessageHandler) AcknowledgeMessage(w http.ResponseWriter, r *http.Request) {
	if !h.checkAcknowledgementDeps(w) {
		return
	}
	in, ok := h.acknowledgementRequestContext(w, r)
	if !ok {
		return
	}
	outcome, err := h.acknowledgements.Acknowledge(r.Context(), in)
	if err != nil {
		mapServiceError(w, err)
		return
	}
	h.broadcastAcknowledgement(r.Context(), in.WorkspaceID, in.MessageID, outcome)
	httputil.WriteJSON(w, http.StatusOK, mapToAcknowledgementJSON(in.MessageID, outcome.Summary))
}

// broadcastAcknowledgement announces a committed transition to the conversation
// it happened in.
//
// Reached only after the store returned, so nothing is announced that the
// database did not accept. A call that changed nothing — a retry, a double
// click, a row some other transition already resolved — announces nothing
// either: the caller's answer is the same, but there is no change for the
// conversation to hear about, and a retry storm must not become an event storm.
//
// Best-effort, like every other broadcast in this handler: the write already
// succeeded, so a missing publisher or a bus failure costs a subscriber one
// stale summary until its next reconciliation, not the acknowledgement.
func (h *MessageHandler) broadcastAcknowledgement(
	ctx context.Context, workspaceID, messageID string, outcome service.AcknowledgeOutcome,
) {
	if h.acknowledgementBroadcaster == nil || !outcome.Changed {
		return
	}
	h.acknowledgementBroadcaster.PublishAcknowledgementUpdated(
		ctx, workspaceID, outcome.Route.TargetType, outcome.Route.TargetID, messageID,
	)
}

// GetMessageAcknowledgement handles
// GET /api/chat/messages/{messageID}/acknowledgement.
//
// Reads the counts, the caller's own state and, for the sender, the
// per-recipient detail. It is a read in every sense: opening a message is not
// confirming it (#820 keeps DELIVERED, READ and ACKNOWLEDGED apart), and this
// endpoint writes nothing that could blur the two.
func (h *MessageHandler) GetMessageAcknowledgement(w http.ResponseWriter, r *http.Request) {
	if !h.checkAcknowledgementDeps(w) {
		return
	}
	in, ok := h.acknowledgementRequestContext(w, r)
	if !ok {
		return
	}
	summary, err := h.acknowledgements.Read(r.Context(), in)
	if err != nil {
		mapServiceError(w, err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, mapToAcknowledgementJSON(in.MessageID, summary))
}

// GetMessageAcknowledgements handles
// POST /api/chat/messages/acknowledgements.
//
// One request for a whole page. Before this, a conversation holding twenty
// messages that asked for confirmation cost twenty round trips and twenty
// aggregations on open and again on every reconnect; now it costs one of each.
//
// POST despite being a read, for the same reason RouteMessageLinkSafetyStatus
// is: the request is a batch of ids. It spends the list budget, which is the
// budget for reading a page.
//
// The per-recipient detail is deliberately not here — see the single read.
func (h *MessageHandler) GetMessageAcknowledgements(w http.ResponseWriter, r *http.Request) {
	if !h.checkAcknowledgementDeps(w) {
		return
	}
	userID := GetContextUserID(r)
	if userID == "" {
		httputil.WriteError(w, http.StatusUnauthorized, httputil.ErrCodeUnauthorized, "unauthorized")
		return
	}
	var req acknowledgementBatchRequest
	if !decodeStrictJSON(w, r, &req) {
		return
	}
	wsID, ok := h.resolveWorkspaceID(r.Context(), w)
	if !ok {
		return
	}
	summaries, err := h.acknowledgements.ReadBatch(r.Context(), service.ReadAcknowledgementBatchInput{
		WorkspaceID: wsID, MessageIDs: req.MessageIDs, ViewerID: userID,
	})
	if err != nil {
		mapServiceError(w, err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, mapToAcknowledgementBatchJSON(summaries))
}

func mapToAcknowledgementBatchJSON(
	summaries map[string]domain.AcknowledgementSummary,
) acknowledgementBatchResponseData {
	response := acknowledgementBatchResponseData{
		Acknowledgements: make(map[string]acknowledgementJSON, len(summaries)),
	}
	for messageID, summary := range summaries {
		response.Acknowledgements[messageID] = mapToAcknowledgementJSON(messageID, summary)
	}
	return response
}

func mapToAcknowledgementJSON(messageID string, summary domain.AcknowledgementSummary) acknowledgementJSON {
	out := acknowledgementJSON{
		MessageID:    messageID,
		Required:     summary.Required,
		Total:        summary.Total,
		Pending:      summary.Pending,
		Acknowledged: summary.Acknowledged,
		Responded:    summary.Responded,
		Expired:      summary.Expired,
		Cancelled:    summary.Cancelled,
		ViewerState:  string(summary.ViewerState),
	}
	for _, recipient := range summary.Recipients {
		out.Recipients = append(out.Recipients, acknowledgementRecipientJSON{
			RecipientID: recipient.RecipientID,
			State:       string(recipient.State),
			ResolvedAt:  optionalTime(recipient.ResolvedAt),
		})
	}
	return out
}

// optionalTime renders a zero time.Time as JSON null. A recipient who is still
// pending has no resolution instant, and the schema refuses to store one for
// them; saying so as null rather than as 0001-01-01 is what keeps a client from
// having to know that.
func optionalTime(at time.Time) *time.Time {
	if at.IsZero() {
		return nil
	}
	return &at
}
