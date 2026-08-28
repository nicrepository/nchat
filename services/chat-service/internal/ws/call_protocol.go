package ws

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
)

const (
	defaultCallStartLimit  = 10
	defaultCallStartWindow = 60
)

var (
	ErrCallFeatureDisabled = errors.New("call feature disabled")
	ErrCallRateLimited     = errors.New("call rate limited")
)

type StartCallCommand struct {
	WorkspaceID string
	RequestID   string
	CallerID    string
	CalleeID    string
	TargetType  TargetType
	TargetID    string
	Type        domain.CallType
}

type CallHandler interface {
	// StartCall additionally returns the ParticipationID a resource
	// admission holds (issue #622 round 3) — always empty for a direct call.
	StartCall(context.Context, StartCallCommand) (domain.Call, string, error)
	TransitionCall(context.Context, string, string, string, ClientMessageType) (domain.Call, error)
	CurrentCall(context.Context, string, string, string) (domain.Call, error)
	// RenewCallPresence takes workspaceID, actorID, callID, participationID,
	// in that order (issue #622 round 3) — participationID fences the
	// heartbeat to one specific admission; empty claims the legacy identity.
	RenewCallPresence(context.Context, string, string, string, string) error
	// LeaveCall releases one participant's own presence in a resource call —
	// distinct from TransitionCall's call.end, which only the resource
	// call's original caller may use to end it for everyone (issue #569).
	// workspaceID, actorID, callID, participationID, in that order
	// (participationID added by issue #622 round 3, fencing this leave to
	// one specific admission; empty claims the legacy identity). released is
	// false — with no error — when participationID no longer matches the
	// actor's current lease: a stale admission a newer one has since
	// superseded, or one that was never fenced. The caller must never treat
	// released=false as a genuine departure of the CURRENT participation.
	LeaveCall(ctx context.Context, workspaceID, actorID, callID, participationID string) (call domain.Call, released bool, err error)
	// ResourceSync answers call.resource.sync: the authoritative active call
	// (if any) of one channel/group-DM target, requester-only, never
	// creating a lease or a call (issue #622). workspaceID, actorID,
	// targetType, targetID, in that order. found is false for both "no
	// active call" and "not authorized for this target" — the two must be
	// indistinguishable on the wire (see ResourceSync's storage-layer
	// counterpart, which keeps them distinguishable internally).
	// observedAt orders this answer against a call that starts concurrently:
	// it must never be later than the created_at of a call that had not yet
	// committed when this read happened.
	ResourceSync(ctx context.Context, workspaceID, actorID string, targetType TargetType, targetID string) (call domain.Call, found bool, observedAt time.Time, err error)
	// JoinCall admits actorID into an already-known, active resource call —
	// never creates one (issue #622). workspaceID, actorID, callID,
	// targetType, targetID, in that order; targetType/targetID are the
	// client's claim and are revalidated against the call's own persisted
	// target before anything is authorized. Returns the fresh
	// ParticipationID this admission rotated the lease to (issue #622 round
	// 3).
	JoinCall(ctx context.Context, workspaceID, actorID, callID string, targetType TargetType, targetID string) (call domain.Call, participationID string, err error)
}

type CallLimiter interface {
	AllowActionWithLimit(context.Context, string, string, int, int) (bool, error)
}

func WithCallHandler(handler CallHandler) HubOption {
	return func(h *Hub) { h.callHandler = handler }
}

func WithCallLimiter(limiter CallLimiter, maxActions, windowSeconds int) HubOption {
	return func(h *Hub) {
		h.callLimiter = limiter
		h.callStartLimit = maxActions
		h.callStartWindow = windowSeconds
	}
}

func (h *Hub) handleCallMessage(ctx context.Context, c *Client, msg ClientMessage) error {
	if h.callHandler == nil {
		return ErrCallFeatureDisabled
	}
	switch msg.Type {
	case ClientMessageTypeCallStart:
		direct := msg.TargetUserID != "" && msg.TargetType == "" && msg.TargetID == ""
		resource := msg.TargetUserID == "" && (msg.TargetType == TargetTypeChannel || msg.TargetType == TargetTypeDM) && msg.TargetID != ""
		if msg.CallID != "" || msg.MessageID != "" || msg.Emoji != "" || msg.RequestID == "" ||
			msg.ParticipationID != "" || (!direct && !resource) || !msg.CallType.Valid() {
			return domain.ErrInvalidInput
		}
		requestID, err := canonicalCallUUID(msg.RequestID)
		if err != nil {
			return domain.ErrInvalidInput
		}
		calleeID, targetID := "", ""
		if direct {
			calleeID, err = canonicalCallUUID(msg.TargetUserID)
		} else {
			targetID, err = canonicalCallUUID(msg.TargetID)
		}
		if err != nil {
			return domain.ErrInvalidInput
		}
		if h.callLimiter == nil {
			return ErrCallFeatureDisabled
		}
		limit, window := h.callStartLimit, h.callStartWindow
		if limit <= 0 {
			limit = defaultCallStartLimit
		}
		if window <= 0 {
			window = defaultCallStartWindow
		}
		allowed, err := h.callLimiter.AllowActionWithLimit(ctx, c.userID, "call_start", limit, window)
		if err != nil {
			return fmt.Errorf("%w: %v", ErrCallFeatureDisabled, err)
		}
		if !allowed {
			return ErrCallRateLimited
		}
		call, participationID, err := h.callHandler.StartCall(ctx, StartCallCommand{
			WorkspaceID: c.workspaceID,
			RequestID:   requestID,
			CallerID:    c.userID,
			CalleeID:    calleeID,
			TargetType:  msg.TargetType,
			TargetID:    targetID,
			Type:        msg.CallType,
		})
		if err != nil {
			return err
		}
		if resource {
			// call.admitted is the command-response ACK for this resource
			// call.start, correlated by request_id — separate from the
			// call.accepted lifecycle broadcast PublishCall already sent
			// inside the handler above (issue #622). Direct call.start keeps
			// its existing behavior: no ACK, unchanged from before #622.
			h.sendCallAdmitted(c, ClientMessageTypeCallStart, requestID, call, participationID)
		}
		return nil

	case ClientMessageTypeCallResourceSync:
		if msg.RequestID != "" || msg.TargetUserID != "" || msg.CallType != "" || msg.CallID != "" ||
			msg.MessageID != "" || msg.Emoji != "" || msg.SyncID == "" ||
			(msg.TargetType != TargetTypeChannel && msg.TargetType != TargetTypeDM) || msg.TargetID == "" {
			return domain.ErrInvalidInput
		}
		syncID, err := canonicalCallUUID(msg.SyncID)
		if err != nil {
			return domain.ErrInvalidInput
		}
		targetID, err := canonicalCallUUID(msg.TargetID)
		if err != nil {
			return domain.ErrInvalidInput
		}
		call, found, observedAt, err := h.callHandler.ResourceSync(ctx, c.workspaceID, c.userID, msg.TargetType, targetID)
		if err != nil {
			return err
		}
		h.sendResourceSynced(c, syncID, msg.TargetType, targetID, call, found, observedAt)
		return nil

	case ClientMessageTypeCallJoin:
		if msg.TargetUserID != "" || msg.CallType != "" || msg.MessageID != "" || msg.Emoji != "" ||
			msg.RequestID == "" || msg.CallID == "" || msg.ParticipationID != "" ||
			(msg.TargetType != TargetTypeChannel && msg.TargetType != TargetTypeDM) || msg.TargetID == "" {
			return domain.ErrInvalidInput
		}
		requestID, err := canonicalCallUUID(msg.RequestID)
		if err != nil {
			return domain.ErrInvalidInput
		}
		joinCallID, err := canonicalCallUUID(msg.CallID)
		if err != nil {
			return domain.ErrInvalidInput
		}
		targetID, err := canonicalCallUUID(msg.TargetID)
		if err != nil {
			return domain.ErrInvalidInput
		}
		call, participationID, err := h.callHandler.JoinCall(ctx, c.workspaceID, c.userID, joinCallID, msg.TargetType, targetID)
		if err != nil {
			return err
		}
		h.sendCallAdmitted(c, ClientMessageTypeCallJoin, requestID, call, participationID)
		return nil

	case ClientMessageTypeCallPresence:
		if msg.RequestID != "" || msg.TargetUserID != "" || msg.CallType != "" || msg.TargetType != "" ||
			msg.TargetID != "" || msg.MessageID != "" || msg.Emoji != "" {
			return domain.ErrInvalidInput
		}
		callID, err := canonicalCallUUID(msg.CallID)
		if err != nil {
			return domain.ErrInvalidInput
		}
		// ParticipationID fences this heartbeat to one specific admission
		// (issue #622 round 3) — empty claims the pre-fencing legacy
		// identity, validated (if non-empty) as a canonical UUID by
		// CallService.Presence itself. A stale value renews nothing and
		// surfaces as call_participation_stale below, never as a silent
		// success and never as a lifecycle broadcast.
		return h.callHandler.RenewCallPresence(ctx, c.workspaceID, c.userID, callID, msg.ParticipationID)

	case ClientMessageTypeCallAccept, ClientMessageTypeCallDecline, ClientMessageTypeCallCancel, ClientMessageTypeCallEnd:
		if msg.RequestID != "" || msg.TargetUserID != "" || msg.CallType != "" || msg.TargetType != "" ||
			msg.TargetID != "" || msg.MessageID != "" || msg.Emoji != "" {
			return domain.ErrInvalidInput
		}
		callID, err := canonicalCallUUID(msg.CallID)
		if err != nil {
			return domain.ErrInvalidInput
		}
		_, err = h.callHandler.TransitionCall(ctx, c.workspaceID, c.userID, callID, msg.Type)
		return err

	case ClientMessageTypeCallLeave:
		// RequestID is now required (issue #622 round 3) — it correlates
		// this leave's own call_participation_stale error, exactly like a
		// resource call.start/call.join's request_id already correlates
		// theirs (see callResponseTo). Direct calls never send call.leave at
		// all (LeaveCall/LeaveResourceCall reject a non-resource call
		// outright), so this is never a compatibility concern for RF-23.
		if msg.RequestID == "" || msg.TargetUserID != "" || msg.CallType != "" || msg.TargetType != "" ||
			msg.TargetID != "" || msg.MessageID != "" || msg.Emoji != "" {
			return domain.ErrInvalidInput
		}
		if _, err := canonicalCallUUID(msg.RequestID); err != nil {
			return domain.ErrInvalidInput
		}
		callID, err := canonicalCallUUID(msg.CallID)
		if err != nil {
			return domain.ErrInvalidInput
		}
		call, released, err := h.callHandler.LeaveCall(ctx, c.workspaceID, c.userID, callID, msg.ParticipationID)
		if err != nil {
			return err
		}
		if !released {
			// This request's own participation_id no longer matches the
			// actor's current lease (issue #622 round 3) — nothing was
			// released, nothing about the call changed. Reported as a
			// distinct call.error correlated by requestID (see
			// callResponseTo), never as the same call.leave reply a genuine
			// release gets and never as a lifecycle broadcast: the
			// requester must be able to tell "my own, specific leave was a
			// no-op" from "the call actually changed", so it never mistakes
			// this for its CURRENT participation having ended.
			return domain.ErrCallParticipationStale
		}
		// Explicit requester-only command result. Never reuse a lifecycle event
		// as this ACK: only PublishCall may announce lifecycle changes.
		h.sendCallLeft(c, msg.RequestID, call)
		return nil

	case ClientMessageTypeCallSync:
		if msg.RequestID != "" || msg.TargetUserID != "" || msg.CallType != "" ||
			msg.TargetType != "" || msg.TargetID != "" || msg.MessageID != "" || msg.Emoji != "" {
			return domain.ErrInvalidInput
		}
		callID := ""
		if msg.CallID != "" {
			var err error
			callID, err = canonicalCallUUID(msg.CallID)
			if err != nil {
				return domain.ErrInvalidInput
			}
		}
		// SyncID is optional (issue #614 blocker follow-up), unlike
		// call.resource.sync's mandatory one: a legacy client that omits it
		// keeps getting the pre-existing sendCallToClient reply below,
		// unchanged. A client that supplies one gets a distinct,
		// requester-only call.synced reply correlated by it — the plain
		// lifecycle-event shape sendCallToClient sends is wire-identical to
		// a real broadcast for the same call_id (and to another concurrent
		// call.sync's own reply), so it can never be safely correlated to
		// one specific request. See resolveCall in
		// apps/web/src/chat/resourceCallSignaling.ts.
		syncID := ""
		if msg.SyncID != "" {
			var err error
			syncID, err = canonicalCallUUID(msg.SyncID)
			if err != nil {
				return domain.ErrInvalidInput
			}
		}
		call, err := h.callHandler.CurrentCall(ctx, c.workspaceID, c.userID, callID)
		if err != nil {
			return err
		}
		if syncID != "" {
			h.sendCallSynced(c, syncID, call)
		} else {
			h.sendCallToClient(c, call)
		}
		return nil
	default:
		return domain.ErrInvalidInput
	}
}

func (h *Hub) PublishCall(ctx context.Context, call domain.Call) {
	if call.IsResource() {
		h.publishCallToTarget(ctx, call, TargetType(call.TargetType), call.TargetID)
		return
	}
	h.publishCallToUser(ctx, call, call.CallerID)
	h.publishCallToUser(ctx, call, call.CalleeID)
}

func (h *Hub) publishCallToUser(ctx context.Context, call domain.Call, userID string) {
	h.publishCallToTarget(ctx, call, TargetTypeUser, userID)
}

func (h *Hub) publishCallToTarget(ctx context.Context, call domain.Call, targetType TargetType, targetID string) {
	event, ok := h.newCallEvent(call, targetType, targetID)
	if !ok {
		return
	}
	data, err := json.Marshal(event)
	if err != nil {
		h.logger.ErrorContext(ctx, "ws: marshal call event", "call_id", call.ID, "error", err)
		return
	}
	select {
	case h.bcast <- broadcastReq{event: event, data: data}:
	case <-ctx.Done():
		return
	case <-h.quit:
		return
	}
	if err := h.bus.Publish(ctx, event); err != nil {
		h.logger.WarnContext(ctx, "ws: call bus publish failed", "call_id", call.ID, "event_type", string(event.Type))
	}
}

func (h *Hub) sendCallToClient(c *Client, call domain.Call) {
	targetType, targetID := TargetTypeUser, c.userID
	if call.IsResource() {
		targetType, targetID = TargetType(call.TargetType), call.TargetID
	}
	event, ok := h.newCallEvent(call, targetType, targetID)
	if !ok {
		return
	}
	data, err := json.Marshal(event)
	if err == nil {
		_ = c.enqueue(data)
	}
}

func (h *Hub) newCallEvent(call domain.Call, targetType TargetType, targetID string) (Event, bool) {
	eventType, ok := callEventType(call.Status)
	if !ok {
		return Event{}, false
	}
	payload := callEventPayload(call)
	return Event{
		SchemaVersion:    CurrentEventSchemaVersion,
		Type:             eventType,
		WorkspaceID:      call.WorkspaceID,
		TargetType:       targetType,
		TargetID:         targetID,
		Call:             &payload,
		EventID:          uuid.NewString(),
		SourceInstanceID: h.presenceInstanceID,
		CreatedAt:        call.UpdatedAt,
	}, true
}

func callEventPayload(call domain.Call) CallEventPayload {
	return CallEventPayload{
		ID: call.ID, RequestID: call.RequestID, CallerID: call.CallerID, CalleeID: call.CalleeID,
		TargetType: call.TargetType, TargetID: call.TargetID,
		CallType: call.Type, Status: call.Status, Version: call.Version,
		CreatedAt: call.CreatedAt, OccurredAt: call.UpdatedAt, ExpiresAt: call.ExpiresAt,
		AcceptedAt: call.AcceptedAt, EndedAt: call.EndedAt,
	}
}

// callAdmittedResponse is the requester-only command-response ACK for a
// resource call.start or a call.join (issue #622) — distinct from the
// call.accepted/call.ringing lifecycle broadcast PublishCall sends to every
// participant. response_to correlates it to the request_id of the command
// that produced it; never confused with a lifecycle event, and never sent to
// anyone but the requester.
//
// ParticipationID (issue #622 round 3) is the opaque fencing token THIS
// admission alone now holds — deliberately its own sibling field, never a
// member of CallEventPayload/Call: it belongs to the requester's own
// admission, not to the call's public lifecycle, and must never appear in
// any lifecycle broadcast or call.resource.synced answer an observer could
// see. Empty only for a direct call.start, which is never fenced; always
// non-empty for a resource call.start/call.join.
type callAdmittedResponse struct {
	Type            string           `json:"type"`
	Operation       string           `json:"operation"`
	ResponseTo      string           `json:"response_to"`
	Call            CallEventPayload `json:"call"`
	ParticipationID string           `json:"participation_id,omitempty"`
}

type callLeftResponse struct {
	Type       string           `json:"type"`
	Operation  string           `json:"operation"`
	ResponseTo string           `json:"response_to"`
	Released   bool             `json:"released"`
	Call       CallEventPayload `json:"call"`
}

func (h *Hub) sendCallAdmitted(c *Client, operation ClientMessageType, responseTo string, call domain.Call, participationID string) {
	data, err := json.Marshal(callAdmittedResponse{
		Type: "call.admitted", Operation: string(operation), ResponseTo: responseTo,
		Call: callEventPayload(call), ParticipationID: participationID,
	})
	if err == nil {
		_ = c.enqueue(data)
	}
}

func (h *Hub) sendCallLeft(c *Client, responseTo string, call domain.Call) {
	data, err := json.Marshal(callLeftResponse{
		Type: "call.left", Operation: string(ClientMessageTypeCallLeave), ResponseTo: responseTo,
		Released: true, Call: callEventPayload(call),
	})
	if err == nil {
		_ = c.enqueue(data)
	}
}

// callResourceSyncedResponse is the requester-only answer to
// call.resource.sync (issue #622): the authoritative active call of one
// channel/group-DM target, or null. Never broadcast. sync_id correlates it to
// the request. Call is nil for both "no active call" and "not authorized for
// this target" — the two are indistinguishable on the wire by design (see
// CallHandler.ResourceSync).
//
// ObservedAt orders this answer against a call that starts concurrently: a
// future client must never let an in-flight null response, once it lands,
// override a call it already learned about from a broadcast that occurred
// after this response's snapshot. See storage.PGXCallStore.ActiveResourceCall
// for how it is derived to make that safe.
type callResourceSyncedResponse struct {
	Type       string            `json:"type"`
	SyncID     string            `json:"sync_id"`
	TargetType TargetType        `json:"target_type"`
	TargetID   string            `json:"target_id"`
	Call       *CallEventPayload `json:"call"`
	ObservedAt time.Time         `json:"observed_at"`
}

func (h *Hub) sendResourceSynced(c *Client, syncID string, targetType TargetType, targetID string, call domain.Call, found bool, observedAt time.Time) {
	response := callResourceSyncedResponse{
		Type: "call.resource.synced", SyncID: syncID,
		TargetType: targetType, TargetID: targetID, ObservedAt: observedAt,
	}
	if found {
		payload := callEventPayload(call)
		response.Call = &payload
	}
	data, err := json.Marshal(response)
	if err == nil {
		_ = c.enqueue(data)
	}
}

// callSyncedResponse is the requester-only, sync_id-correlated answer to a
// call.sync that supplied one (issue #614 blocker follow-up) — a direct
// counterpart to callResourceSyncedResponse above, for the direct/authenticated
// call.sync path. Never broadcast. Deliberately carries nothing beyond `call`
// (no participants, token, or media): identical payload shape to every other
// call.sync reply, just correlated instead of shaped like a lifecycle event.
type callSyncedResponse struct {
	Type   string           `json:"type"`
	SyncID string           `json:"sync_id"`
	Call   CallEventPayload `json:"call"`
}

func (h *Hub) sendCallSynced(c *Client, syncID string, call domain.Call) {
	data, err := json.Marshal(callSyncedResponse{
		Type: "call.synced", SyncID: syncID, Call: callEventPayload(call),
	})
	if err == nil {
		_ = c.enqueue(data)
	}
}

func callEventType(status domain.CallStatus) (EventType, bool) {
	switch status {
	case domain.CallStatusRinging:
		return EventTypeCallRinging, true
	case domain.CallStatusActive:
		return EventTypeCallAccepted, true
	case domain.CallStatusDeclined:
		return EventTypeCallDeclined, true
	case domain.CallStatusCancelled:
		return EventTypeCallCancelled, true
	case domain.CallStatusTimedOut:
		return EventTypeCallTimedOut, true
	case domain.CallStatusEnded:
		return EventTypeCallEnded, true
	default:
		return "", false
	}
}

func canonicalCallUUID(value string) (string, error) {
	id, err := uuid.Parse(value)
	if err != nil {
		return "", err
	}
	return id.String(), nil
}

// handleCallClientError writes the requester-only call.error for a failed
// call command. responseTo is non-empty for every command call.start
// (direct or resource, issue #615) — call.join and call.leave (issue #622
// round 3) — and call.resource.sync/call.sync (via sync_id) have
// correlation for. Every other pre-existing call command keeps responseTo
// empty, unchanged. See callResponseTo.
func handleCallClientError(c *Client, operation ClientMessageType, callID string, responseTo string, callErr error) bool {
	response := clientErrorResponse{Type: "call.error", Operation: string(operation), CallID: callID, ResponseTo: responseTo}
	switch {
	case errors.Is(callErr, ErrCallRateLimited):
		response.Code, response.RetryAfter = "call_rate_limited", defaultCallStartWindow
	case errors.Is(callErr, ErrCallFeatureDisabled):
		response.Code = "call_unavailable"
	case errors.Is(callErr, domain.ErrInvalidInput):
		response.Code = "call_invalid"
	case errors.Is(callErr, domain.ErrNotFound), errors.Is(callErr, domain.ErrForbidden):
		response.Code = "call_not_found"
	case errors.Is(callErr, domain.ErrCallParticipantBusy):
		// Must be checked before the generic ErrConflict case below:
		// ErrCallParticipantBusy wraps ErrConflict, so it would otherwise
		// match that broader case first and be misreported as a lifecycle
		// state conflict (issue #575).
		response.Code = "call_participant_busy"
	case errors.Is(callErr, domain.ErrCallParticipationStale):
		// Same reasoning as ErrCallParticipantBusy above: must be checked
		// before the generic ErrConflict case, and is its own distinct code
		// — never call_invalid_state (this is not a lifecycle conflict) and
		// never call_participant_busy (issue #622 round 3: the actor is not
		// "busy elsewhere", its own claimed fencing token for THIS call is
		// simply no longer current).
		response.Code = "call_participation_stale"
	case errors.Is(callErr, domain.ErrConflict):
		response.Code = "call_invalid_state"
	default:
		return false
	}
	data, err := json.Marshal(response)
	return err == nil && c.enqueue(data)
}

// callResponseTo derives the response_to a failed call command's call.error
// should carry (issue #622; call.leave added by round 3; direct call.start
// added by issue #615). call.start now reuses its own request_id for BOTH
// direct and resource — previously only resource call.start did (identified
// by carrying target_user_id, which no resource command ever does), and a
// direct call.start's own error carried no correlation at all. Without it,
// useCallSignaling's errorMatchesPending could only correlate a direct
// call.start error by "is a call.start currently pending" (there is at most
// one in flight per hook instance), which let a stale call.error from an
// abandoned attempt A be mistaken for a different, later attempt B's own
// failure — exactly the race issue #615 requires closed, mirroring the
// success path's existing correlation (call.ringing already echoes
// request_id via Call.RequestID, used by eventCompletesPending). call.join
// and call.leave reuse their own request_id, and call.resource.sync/
// call.sync (when the client supplies sync_id) use it instead. Every other
// existing call command carries none, unchanged.
func callResponseTo(msg ClientMessage) string {
	switch msg.Type {
	case ClientMessageTypeCallStart:
		return msg.RequestID
	case ClientMessageTypeCallJoin, ClientMessageTypeCallLeave:
		return msg.RequestID
	case ClientMessageTypeCallResourceSync:
		return msg.SyncID
	case ClientMessageTypeCallSync:
		// Empty for a legacy client that omitted sync_id (unchanged from
		// before #614's blocker follow-up) — response.ResponseTo has
		// omitempty, so its call.error stays wire-identical to before.
		return msg.SyncID
	default:
		return ""
	}
}
