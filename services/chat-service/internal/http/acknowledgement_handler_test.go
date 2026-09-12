package httpapi_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	httpapi "github.com/nicrepository/nchat/services/chat-service/internal/http"
	"github.com/nicrepository/nchat/services/chat-service/internal/service"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

// Issue #824, the HTTP contract. Two endpoints, one path, no request body: what
// is asserted here is the status for each outcome, the fact that neither
// endpoint reads a recipient from the client, and that the refusals are
// indistinguishable from one another.

type fakeAcknowledgementProvider struct {
	summary domain.AcknowledgementSummary
	route   storage.AcknowledgementRoute
	changed bool
	err     error

	batch            map[string]domain.AcknowledgementSummary
	acknowledgeCalls int
	readCalls        int
	batchCalls       int
	lastInput        service.AcknowledgementActionInput
	lastBatch        service.ReadAcknowledgementBatchInput
}

func (f *fakeAcknowledgementProvider) Acknowledge(
	_ context.Context, in service.AcknowledgementActionInput,
) (service.AcknowledgeOutcome, error) {
	f.acknowledgeCalls++
	f.lastInput = in
	if f.err != nil {
		return service.AcknowledgeOutcome{}, f.err
	}
	return service.AcknowledgeOutcome{Summary: f.summary, Route: f.route, Changed: f.changed}, nil
}

// acknowledgementBroadcast records one published invalidation, so a test can
// assert both what it carried and — just as important — what it did not.
type acknowledgementBroadcast struct {
	WorkspaceID string
	TargetType  string
	TargetID    string
	MessageID   string
}

type fakeAcknowledgementBroadcaster struct {
	published []acknowledgementBroadcast
}

func (f *fakeAcknowledgementBroadcaster) PublishAcknowledgementUpdated(
	_ context.Context, workspaceID, targetType, targetID, messageID string,
) {
	f.published = append(f.published, acknowledgementBroadcast{
		WorkspaceID: workspaceID, TargetType: targetType, TargetID: targetID, MessageID: messageID,
	})
}

func (f *fakeAcknowledgementProvider) ReadBatch(
	_ context.Context, in service.ReadAcknowledgementBatchInput,
) (map[string]domain.AcknowledgementSummary, error) {
	f.batchCalls++
	f.lastBatch = in
	return f.batch, f.err
}

func (f *fakeAcknowledgementProvider) Read(
	_ context.Context, in service.AcknowledgementActionInput,
) (domain.AcknowledgementSummary, error) {
	f.readCalls++
	f.lastInput = in
	return f.summary, f.err
}

// acknowledgementRequest issues one call against a handler wired with the given
// fake, using the shared authenticated-request helper.
func acknowledgementRequest(
	t *testing.T, method string, acks *fakeAcknowledgementProvider, body string,
) *httptest.ResponseRecorder {
	t.Helper()
	return acknowledgementRequestWith(t, method, acks, &fakeAcknowledgementBroadcaster{}, body)
}

func acknowledgementRequestWith(
	t *testing.T, method string, acks *fakeAcknowledgementProvider,
	broadcaster *fakeAcknowledgementBroadcaster, body string,
) *httptest.ResponseRecorder {
	t.Helper()
	h := makeHandlerWithUser(&fakeWorkspaceResolver{workspace: activeWorkspace()}, &fakeMessageProvider{}).
		WithAcknowledgements(acks, broadcaster)
	rec := httptest.NewRecorder()
	r := requestWithUser(method, "/api/chat/messages/"+testMessageID+"/acknowledgement", strings.NewReader(body))
	r.SetPathValue("messageID", testMessageID)
	if method == http.MethodPost {
		h.AcknowledgeMessage(rec, r)
	} else {
		h.GetMessageAcknowledgement(rec, r)
	}
	return rec
}

func pendingSummary() domain.AcknowledgementSummary {
	return domain.AcknowledgementSummary{
		Required: true, Total: 7, Pending: 3, Acknowledged: 4,
		ViewerState: domain.AcknowledgementStatePending,
	}
}

// A recipient confirming gets 200 and the state that now holds, so a client can
// render the authoritative answer without a second request.
func TestMessageHandler_AcknowledgeMessage_ReturnsTheResultingState(t *testing.T) {
	summary := pendingSummary()
	summary.ViewerState = domain.AcknowledgementStateAcknowledged
	summary.Acknowledged, summary.Pending = 5, 2
	acks := &fakeAcknowledgementProvider{summary: summary}

	rec := acknowledgementRequest(t, http.MethodPost, acks, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d — body: %s", rec.Code, rec.Body.String())
	}
	data := decodeBody(t, rec)["data"].(map[string]any)
	if data["viewer_state"] != string(domain.AcknowledgementStateAcknowledged) {
		t.Fatalf("viewer_state = %#v, want acknowledged", data["viewer_state"])
	}
	if data["acknowledged"] != float64(5) || data["pending"] != float64(2) || data["total"] != float64(7) {
		t.Fatalf("counts = %#v, want the summary the service returned", data)
	}
	if data["message_id"] != testMessageID {
		t.Fatalf("message_id = %#v, want the message named in the path", data["message_id"])
	}
}

// Two identical calls are two identical answers. The handler holds no state of
// its own, so the idempotency it reports is the store's, and this is what the
// double-click case looks like from the outside.
func TestMessageHandler_AcknowledgeMessage_RepeatedCallIsTheSameAnswer(t *testing.T) {
	summary := pendingSummary()
	summary.ViewerState = domain.AcknowledgementStateAcknowledged
	acks := &fakeAcknowledgementProvider{summary: summary}

	first := acknowledgementRequest(t, http.MethodPost, acks, "")
	second := acknowledgementRequest(t, http.MethodPost, acks, "")
	if first.Code != http.StatusOK || second.Code != http.StatusOK {
		t.Fatalf("expected 200 twice, got %d and %d", first.Code, second.Code)
	}
	if first.Body.String() != second.Body.String() {
		t.Fatalf("a repeated acknowledgement answered differently:\n%s\n%s", first.Body, second.Body)
	}
	if acks.acknowledgeCalls != 2 {
		t.Fatalf("service calls = %d, want 2 — the handler must not cache the answer", acks.acknowledgeCalls)
	}
}

// The recipient the service is asked about is the authenticated user, and a
// body that tries to name somebody else changes nothing — POST carries no
// payload contract at all.
func TestMessageHandler_AcknowledgeMessage_IgnoresAnyClientSuppliedRecipient(t *testing.T) {
	acks := &fakeAcknowledgementProvider{summary: pendingSummary()}

	rec := acknowledgementRequest(t, http.MethodPost, acks,
		`{"recipient_id":"99999999-9999-4999-8999-999999999999","state":"acknowledged"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d — body: %s", rec.Code, rec.Body.String())
	}
	if acks.lastInput.ActorUserID != msgTestUserID {
		t.Fatalf("service asked about %q, want the authenticated user", acks.lastInput.ActorUserID)
	}
}

// Every refusal is the same 404: a message that does not exist, one the caller
// may not read, and one that never asked them are deliberately identical.
func TestMessageHandler_AcknowledgeMessage_RefusalsAreIndistinguishable(t *testing.T) {
	acks := &fakeAcknowledgementProvider{err: domain.ErrNotFound}

	for _, method := range []string{http.MethodPost, http.MethodGet} {
		rec := acknowledgementRequest(t, method, acks, "")
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s: expected 404, got %d — body: %s", method, rec.Code, rec.Body.String())
		}
		if strings.Contains(strings.ToLower(rec.Body.String()), "recipient") {
			t.Fatalf("%s: the refusal describes recipients: %s", method, rec.Body)
		}
	}
}

// A malformed message id is refused at the boundary, before any service call:
// nothing is looked up, so nothing can be learned from the attempt.
func TestMessageHandler_AcknowledgeMessage_RejectsAMalformedMessageID(t *testing.T) {
	for _, id := range []string{"not-a-uuid", "", "00000000-0000-0000-0000-000000000000"} {
		acks := &fakeAcknowledgementProvider{summary: pendingSummary()}
		h := makeHandlerWithUser(&fakeWorkspaceResolver{workspace: activeWorkspace()}, &fakeMessageProvider{}).
			WithAcknowledgements(acks, &fakeAcknowledgementBroadcaster{})
		rec := httptest.NewRecorder()
		r := requestWithUser(http.MethodPost, "/api/chat/messages/"+id+"/acknowledgement", nil)
		r.SetPathValue("messageID", id)
		h.AcknowledgeMessage(rec, r)

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("id %q: expected 400, got %d", id, rec.Code)
		}
		if acks.acknowledgeCalls != 0 {
			t.Fatalf("id %q reached the service", id)
		}
	}
}

// No session, no acknowledgement — and no lookup either.
func TestMessageHandler_AcknowledgeMessage_RequiresAnAuthenticatedCaller(t *testing.T) {
	acks := &fakeAcknowledgementProvider{summary: pendingSummary()}
	h := makeHandlerWithUser(&fakeWorkspaceResolver{workspace: activeWorkspace()}, &fakeMessageProvider{}).
		WithAcknowledgements(acks, &fakeAcknowledgementBroadcaster{})
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/chat/messages/"+testMessageID+"/acknowledgement", nil)
	r.SetPathValue("messageID", testMessageID)
	h.AcknowledgeMessage(rec, r)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
	if acks.acknowledgeCalls != 0 {
		t.Fatal("an unauthenticated request reached the service")
	}
}

// Without the dependency both routes answer 503 rather than panicking, which is
// what a deployment with no database looks like.
func TestMessageHandler_Acknowledgement_AnswersUnavailableWhenNotWired(t *testing.T) {
	h := makeHandlerWithUser(&fakeWorkspaceResolver{workspace: activeWorkspace()}, &fakeMessageProvider{})
	for _, handler := range []http.HandlerFunc{h.AcknowledgeMessage, h.GetMessageAcknowledgement} {
		rec := httptest.NewRecorder()
		r := requestWithUser(http.MethodPost, "/api/chat/messages/"+testMessageID+"/acknowledgement", nil)
		r.SetPathValue("messageID", testMessageID)
		handler(rec, r)
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("expected 503, got %d", rec.Code)
		}
	}
}

// A reader who is not the sender gets counts and their own state, and no
// recipients key at all — the field is omitted rather than sent empty, so a
// client cannot mistake "withheld" for "nobody".
func TestMessageHandler_GetMessageAcknowledgement_OmitsTheDetailForANonSender(t *testing.T) {
	acks := &fakeAcknowledgementProvider{summary: pendingSummary()}

	rec := acknowledgementRequest(t, http.MethodGet, acks, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d — body: %s", rec.Code, rec.Body.String())
	}
	data := decodeBody(t, rec)["data"].(map[string]any)
	if _, present := data["recipients"]; present {
		t.Fatalf("a non-sender received a recipients field: %#v", data["recipients"])
	}
	if data["viewer_state"] != string(domain.AcknowledgementStatePending) {
		t.Fatalf("viewer_state = %#v, want pending", data["viewer_state"])
	}
}

// The sender gets the list, with a resolution instant only where one exists.
func TestMessageHandler_GetMessageAcknowledgement_GivesTheSenderTheDetail(t *testing.T) {
	summary := pendingSummary()
	summary.ViewerState = ""
	summary.Recipients = []domain.AcknowledgementRecipient{
		{RecipientID: "55555555-5555-4555-8555-555555555555",
			State: domain.AcknowledgementStateAcknowledged, ResolvedAt: testNow()},
		{RecipientID: "66666666-6666-4666-8666-666666666666",
			State: domain.AcknowledgementStatePending},
	}
	acks := &fakeAcknowledgementProvider{summary: summary}

	rec := acknowledgementRequest(t, http.MethodGet, acks, "")
	data := decodeBody(t, rec)["data"].(map[string]any)
	recipients, ok := data["recipients"].([]any)
	if !ok || len(recipients) != 2 {
		t.Fatalf("recipients = %#v, want two entries", data["recipients"])
	}
	first := recipients[0].(map[string]any)
	if first["state"] != string(domain.AcknowledgementStateAcknowledged) || first["resolved_at"] == nil {
		t.Fatalf("resolved recipient = %#v, want a state and an instant", first)
	}
	second := recipients[1].(map[string]any)
	if second["resolved_at"] != nil {
		t.Fatalf("a pending recipient carries an instant: %#v", second)
	}
	// No names, addresses or avatars: the sender learns who has not answered
	// from ids they can already resolve, not from a second copy of the directory.
	for key := range first {
		switch key {
		case "recipient_id", "state", "resolved_at":
		default:
			t.Fatalf("recipient detail carries an unexpected field %q", key)
		}
	}
}

// Reading never acknowledges. #820 keeps DELIVERED, READ and ACKNOWLEDGED
// apart, and this is where that separation is visible from the outside.
func TestMessageHandler_GetMessageAcknowledgement_DoesNotAcknowledge(t *testing.T) {
	acks := &fakeAcknowledgementProvider{summary: pendingSummary()}

	acknowledgementRequest(t, http.MethodGet, acks, "")
	if acks.acknowledgeCalls != 0 {
		t.Fatal("reading a message's acknowledgement must not record one")
	}
	if acks.readCalls != 1 {
		t.Fatalf("read calls = %d, want 1", acks.readCalls)
	}
}

// A message that asked nobody is a legitimate answer, not a 404: all counts
// zero, required false, and no state of the viewer's own.
func TestMessageHandler_GetMessageAcknowledgement_ReportsAMessageThatAskedNobody(t *testing.T) {
	acks := &fakeAcknowledgementProvider{summary: domain.AcknowledgementSummary{}}

	rec := acknowledgementRequest(t, http.MethodGet, acks, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	data := decodeBody(t, rec)["data"].(map[string]any)
	if data["required"] != false || data["total"] != float64(0) || data["viewer_state"] != "" {
		t.Fatalf("summary = %#v, want an explicit nothing-was-asked answer", data)
	}
}

// The handler exists on the routing table under the message prefix, for both
// methods and no others.
func TestRouteMessageAcknowledgement_IsMessageScopedAndNamesNoUser(t *testing.T) {
	if httpapi.RouteMessageAcknowledgement != "/api/chat/messages/{messageID}/acknowledgement" {
		t.Fatalf("route = %q", httpapi.RouteMessageAcknowledgement)
	}
	if strings.Contains(httpapi.RouteMessageAcknowledgement, "user") {
		t.Fatal("the route must not name a user: the actor is always the session")
	}
}

// ── the create contract ───────────────────────────────────────────────────────

// Omitting the field is asking for nothing, which is what every client written
// before this issue does.
func TestMessageHandler_CreateChannelMessage_OmittedAcknowledgementAsksForNothing(t *testing.T) {
	rec, msgs := postCreateChannel(t, `{"body_text":"hello"}`, testMessage())
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d — body: %s", rec.Code, rec.Body.String())
	}
	if msgs.lastCreateChannelInput.AcknowledgementRequired {
		t.Fatal("a send that said nothing asked for confirmation")
	}
	data := decodeBody(t, rec)["data"].(map[string]any)
	if data["acknowledgement_required"] != false {
		t.Fatalf("acknowledgement_required = %#v, want an explicit false", data["acknowledgement_required"])
	}
}

// Asking for it reaches the service and is reflected back on the message.
func TestMessageHandler_CreateChannelMessage_ForwardsTheAcknowledgementRequest(t *testing.T) {
	created := testMessage()
	created.AcknowledgementRequired = true
	rec, msgs := postCreateChannel(t,
		`{"body_text":"restart the cluster","priority":"urgent","acknowledgement_required":true}`, created)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d — body: %s", rec.Code, rec.Body.String())
	}
	if !msgs.lastCreateChannelInput.AcknowledgementRequired {
		t.Fatal("the request did not reach the service")
	}
	data := decodeBody(t, rec)["data"].(map[string]any)
	if data["acknowledgement_required"] != true {
		t.Fatalf("acknowledgement_required = %#v, want true", data["acknowledgement_required"])
	}
}

func TestMessageHandler_CreateDMMessage_ForwardsTheAcknowledgementRequest(t *testing.T) {
	created := testMessage()
	created.AcknowledgementRequired = true
	_, msgs := postCreateDM(t, `{"body_text":"please confirm","acknowledgement_required":true}`, created)
	if !msgs.lastCreateDMInput.AcknowledgementRequired {
		t.Fatal("the request did not reach the service on the DM path")
	}
}

// A wrong JSON type is a 400 and nothing is sent. There is no coercion here: a
// string that looks like a boolean is not one.
func TestMessageHandler_CreateChannelMessage_RejectsANonBooleanAcknowledgement(t *testing.T) {
	for _, body := range []string{
		`{"body_text":"hello","acknowledgement_required":"true"}`,
		`{"body_text":"hello","acknowledgement_required":1}`,
		`{"body_text":"hello","acknowledgement_required":[]}`,
	} {
		rec, msgs := postCreateChannel(t, body, testMessage())
		assertRejectedBeforeSending(t, rec, msgs.lastCreateChannelInput.SenderID)
	}
}

// Editing cannot add a confirmation request or withdraw one. The field is
// unknown to the edit payload, and unknown fields are refused — so there is no
// PATCH that touches acknowledgement at all, which is what makes "editing never
// resets an answer" true by construction rather than by care.
func TestMessageHandler_EditMessage_RefusesAnAcknowledgementField(t *testing.T) {
	h := makeHandlerWithUser(&fakeWorkspaceResolver{workspace: activeWorkspace()}, &fakeMessageProvider{}).
		WithEditing(nil, nil, fakeEditLimiter{allowed: true})
	rec := httptest.NewRecorder()
	r := requestWithUser(http.MethodPatch, "/api/chat/messages/"+testMessageID,
		strings.NewReader(`{"body_text":"edited","acknowledgement_required":false}`))
	r.SetPathValue("messageID", testMessageID)
	h.EditMessage(rec, r)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d — body: %s", rec.Code, rec.Body.String())
	}
}

// ── the invalidation broadcast ───────────────────────────────────────────────
//
// Issue #824: a committed acknowledgement tells the conversation to re-read.
// What the event may carry is asserted in the ws package; what is asserted here
// is when it is sent at all, and that it names the right conversation.

func acknowledgedOutcome(changed bool) *fakeAcknowledgementProvider {
	summary := pendingSummary()
	summary.ViewerState = domain.AcknowledgementStateAcknowledged
	return &fakeAcknowledgementProvider{
		summary: summary,
		route:   storage.AcknowledgementRoute{TargetType: "channel", TargetID: testChannelID},
		changed: changed,
	}
}

func TestMessageHandler_AcknowledgeMessage_AnnouncesACommittedChange(t *testing.T) {
	broadcaster := &fakeAcknowledgementBroadcaster{}
	rec := acknowledgementRequestWith(t, http.MethodPost, acknowledgedOutcome(true), broadcaster, "")

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if len(broadcaster.published) != 1 {
		t.Fatalf("published %d events, want exactly 1", len(broadcaster.published))
	}
	published := broadcaster.published[0]
	if published.MessageID != testMessageID {
		t.Fatalf("announced message %q, want the one that changed", published.MessageID)
	}
	if published.TargetType != "channel" || published.TargetID != testChannelID {
		t.Fatalf("announced to %s/%s, want the message's own conversation",
			published.TargetType, published.TargetID)
	}
	if published.WorkspaceID != testWorkspaceID {
		t.Fatalf("announced in workspace %q", published.WorkspaceID)
	}
}

// A retry moved nothing, so there is nothing for the conversation to hear. The
// caller still gets the same 200 and the same state — only the event is absent,
// which is what keeps a double click or a retry storm from becoming an event
// storm.
func TestMessageHandler_AcknowledgeMessage_AnnouncesNothingWhenNothingChanged(t *testing.T) {
	broadcaster := &fakeAcknowledgementBroadcaster{}
	rec := acknowledgementRequestWith(t, http.MethodPost, acknowledgedOutcome(false), broadcaster, "")

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if len(broadcaster.published) != 0 {
		t.Fatalf("a no-op retry announced %d events", len(broadcaster.published))
	}
}

// A refusal announces nothing: the event may only ever follow a committed
// transition.
func TestMessageHandler_AcknowledgeMessage_AnnouncesNothingOnRefusal(t *testing.T) {
	broadcaster := &fakeAcknowledgementBroadcaster{}
	acks := &fakeAcknowledgementProvider{err: domain.ErrNotFound}
	rec := acknowledgementRequestWith(t, http.MethodPost, acks, broadcaster, "")

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
	if len(broadcaster.published) != 0 {
		t.Fatal("a refused acknowledgement announced a change")
	}
}

// Reading never announces anything: it changes nothing.
func TestMessageHandler_GetMessageAcknowledgement_AnnouncesNothing(t *testing.T) {
	broadcaster := &fakeAcknowledgementBroadcaster{}
	acknowledgementRequestWith(t, http.MethodGet, acknowledgedOutcome(true), broadcaster, "")
	if len(broadcaster.published) != 0 {
		t.Fatal("reading a summary announced a change")
	}
}

// Without a broadcaster the endpoint still records the acknowledgement: the
// event is an optimisation over reconnect reconciliation, not the mechanism.
func TestMessageHandler_AcknowledgeMessage_WorksWithoutABroadcaster(t *testing.T) {
	acks := acknowledgedOutcome(true)
	h := makeHandlerWithUser(&fakeWorkspaceResolver{workspace: activeWorkspace()}, &fakeMessageProvider{}).
		WithAcknowledgements(acks, nil)
	rec := httptest.NewRecorder()
	r := requestWithUser(http.MethodPost, "/api/chat/messages/"+testMessageID+"/acknowledgement", nil)
	r.SetPathValue("messageID", testMessageID)
	h.AcknowledgeMessage(rec, r)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if acks.acknowledgeCalls != 1 {
		t.Fatalf("service calls = %d, want 1", acks.acknowledgeCalls)
	}
}

// ── the page batch endpoint ──────────────────────────────────────────────────

func postAcknowledgementBatch(
	t *testing.T, acks *fakeAcknowledgementProvider, body string,
) *httptest.ResponseRecorder {
	t.Helper()
	h := makeHandlerWithUser(&fakeWorkspaceResolver{workspace: activeWorkspace()}, &fakeMessageProvider{}).
		WithAcknowledgements(acks, &fakeAcknowledgementBroadcaster{})
	rec := httptest.NewRecorder()
	r := requestWithUser(http.MethodPost, "/api/chat/messages/acknowledgements", strings.NewReader(body))
	h.GetMessageAcknowledgements(rec, r)
	return rec
}

// The answer is keyed by message id, so a client maps each summary onto the
// message it is drawing without scanning a list.
func TestMessageHandler_GetMessageAcknowledgements_AnswersKeyedByMessage(t *testing.T) {
	other := "66666666-6666-4666-8666-666666666666"
	acks := &fakeAcknowledgementProvider{batch: map[string]domain.AcknowledgementSummary{
		testMessageID: {Required: true, Total: 4, Pending: 1, Acknowledged: 3,
			ViewerState: domain.AcknowledgementStatePending},
		other: {Required: true, Total: 2, Pending: 2},
	}}

	rec := postAcknowledgementBatch(t, acks,
		`{"message_ids":["`+testMessageID+`","`+other+`"]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d — body: %s", rec.Code, rec.Body.String())
	}
	data := decodeBody(t, rec)["data"].(map[string]any)
	entries := data["acknowledgements"].(map[string]any)
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2", len(entries))
	}
	first := entries[testMessageID].(map[string]any)
	if first["acknowledged"] != float64(3) || first["viewer_state"] != "pending" {
		t.Fatalf("entry = %#v", first)
	}
	if first["message_id"] != testMessageID {
		t.Fatalf("entry does not name its message: %#v", first)
	}
	// One call for the whole page, which is the entire point of the endpoint.
	if acks.batchCalls != 1 {
		t.Fatalf("service was asked %d times for one page", acks.batchCalls)
	}
}

// The per-recipient list is not part of the page view. Shipping it here would
// hand every recipient of every asking message on screen to a reader who may
// only be entitled to the counts.
func TestMessageHandler_GetMessageAcknowledgements_CarriesNoRecipientDetail(t *testing.T) {
	acks := &fakeAcknowledgementProvider{batch: map[string]domain.AcknowledgementSummary{
		testMessageID: {Required: true, Total: 2, Pending: 1},
	}}

	rec := postAcknowledgementBatch(t, acks, `{"message_ids":["`+testMessageID+`"]}`)
	body := rec.Body.String()
	for _, forbidden := range []string{"recipients", "recipient_id", "resolved_at"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("the page batch exposes %q: %s", forbidden, body)
		}
	}
}

// A message the caller may not read has no key at all — the same
// non-enumerating answer the single read gives, so mixing ids from another
// conversation into one request reveals nothing about them.
func TestMessageHandler_GetMessageAcknowledgements_OmitsWhatTheCallerMayNotRead(t *testing.T) {
	acks := &fakeAcknowledgementProvider{batch: map[string]domain.AcknowledgementSummary{
		testMessageID: {Required: true, Total: 1, Pending: 1},
	}}

	rec := postAcknowledgementBatch(t, acks,
		`{"message_ids":["`+testMessageID+`","`+otherMessageID+`"]}`)
	entries := decodeBody(t, rec)["data"].(map[string]any)["acknowledgements"].(map[string]any)
	if _, present := entries[otherMessageID]; present {
		t.Fatal("an unreadable message appeared in the answer")
	}
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want only the readable one", len(entries))
	}
}

func TestMessageHandler_GetMessageAcknowledgements_RefusesMalformedRequests(t *testing.T) {
	// What this layer owns is the JSON shape. An empty or oversized id list is
	// the service's rule and is refused there — see the ReadBatch tests — which
	// a fake provider by definition cannot enforce; the mapping of that refusal
	// is asserted separately below.
	for name, body := range map[string]string{
		"not json":      `{`,
		"unknown field": `{"message_ids":["` + testMessageID + `"],"conversation_id":"x"}`,
		"wrong type":    `{"message_ids":"` + testMessageID + `"}`,
	} {
		t.Run(name, func(t *testing.T) {
			acks := &fakeAcknowledgementProvider{}
			rec := postAcknowledgementBatch(t, acks, body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d — body: %s", rec.Code, rec.Body.String())
			}
		})
	}
}

// Malformed ids and oversized batches are the service's rule; the handler maps
// the refusal to the status the rest of the service already uses for one.
func TestMessageHandler_GetMessageAcknowledgements_MapsTheBatchRefusal(t *testing.T) {
	acks := &fakeAcknowledgementProvider{err: domain.ErrInvalidInput}
	rec := postAcknowledgementBatch(t, acks, `{"message_ids":["`+testMessageID+`"]}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestMessageHandler_GetMessageAcknowledgements_RequiresAnAuthenticatedCaller(t *testing.T) {
	acks := &fakeAcknowledgementProvider{}
	h := makeHandlerWithUser(&fakeWorkspaceResolver{workspace: activeWorkspace()}, &fakeMessageProvider{}).
		WithAcknowledgements(acks, &fakeAcknowledgementBroadcaster{})
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/chat/messages/acknowledgements",
		strings.NewReader(`{"message_ids":["`+testMessageID+`"]}`))
	h.GetMessageAcknowledgements(rec, r)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
	if acks.batchCalls != 0 {
		t.Fatal("an unauthenticated request reached the service")
	}
}

// Reading a page never records an acknowledgement.
func TestMessageHandler_GetMessageAcknowledgements_AcknowledgesNothing(t *testing.T) {
	acks := &fakeAcknowledgementProvider{batch: map[string]domain.AcknowledgementSummary{}}
	postAcknowledgementBatch(t, acks, `{"message_ids":["`+testMessageID+`"]}`)
	if acks.acknowledgeCalls != 0 {
		t.Fatal("reading a page recorded an acknowledgement")
	}
}
