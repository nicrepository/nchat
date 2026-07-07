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
)

type fakePinProvider struct {
	pinErr   error
	unpinErr error
	listOut  []domain.PinnedMessage
	listErr  error

	lastPin   service.PinActionInput
	lastUnpin service.PinActionInput
	lastList  service.ListPinsInput
}

func (f *fakePinProvider) Pin(_ context.Context, in service.PinActionInput) error {
	f.lastPin = in
	return f.pinErr
}

func (f *fakePinProvider) Unpin(_ context.Context, in service.PinActionInput) error {
	f.lastUnpin = in
	return f.unpinErr
}

func (f *fakePinProvider) ListPins(_ context.Context, in service.ListPinsInput) ([]domain.PinnedMessage, error) {
	f.lastList = in
	return f.listOut, f.listErr
}

// spyPinBroadcaster records whether/what the handler published.
type spyPinBroadcaster struct {
	calls  int
	pinned bool
	msg    string
}

func (s *spyPinBroadcaster) PublishPinUpdated(_ context.Context, _, _, messageID, _ string, pinned bool) {
	s.calls++
	s.msg = messageID
	s.pinned = pinned
}

func pinHandler(p *fakePinProvider, b *spyPinBroadcaster) *httpapi.MessageHandler {
	return httpapi.NewMessageHandler(
		&fakeWorkspaceResolver{workspace: activeWorkspace()}, &fakeMessageProvider{}, nil,
	).WithPins(p, b)
}

func pinRequest(method string) *http.Request {
	r := requestWithUser(method, "/api/chat/channels/"+testChannelID+"/messages/"+testMessageID+"/pin", nil)
	r.SetPathValue("channelID", testChannelID)
	r.SetPathValue("messageID", testMessageID)
	return r
}

func TestMessageHandler_Pin_WithoutServiceReturns503(t *testing.T) {
	h := httpapi.NewMessageHandler(&fakeWorkspaceResolver{workspace: activeWorkspace()}, &fakeMessageProvider{}, nil)
	rec := httptest.NewRecorder()
	h.PinMessage(rec, pinRequest(http.MethodPost))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 without pin service, got %d", rec.Code)
	}
}

func TestMessageHandler_Pin_Success204AndBroadcasts(t *testing.T) {
	spy := &spyPinBroadcaster{}
	h := pinHandler(&fakePinProvider{}, spy)
	rec := httptest.NewRecorder()
	h.PinMessage(rec, pinRequest(http.MethodPost))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", rec.Code)
	}
	if spy.calls != 1 || spy.msg != testMessageID || !spy.pinned {
		t.Fatalf("expected one pinned broadcast for the message, got calls=%d msg=%q pinned=%v", spy.calls, spy.msg, spy.pinned)
	}
}

func TestMessageHandler_Pin_ForbiddenReturns403_NoBroadcast(t *testing.T) {
	spy := &spyPinBroadcaster{}
	h := pinHandler(&fakePinProvider{pinErr: domain.ErrForbidden}, spy)
	rec := httptest.NewRecorder()
	h.PinMessage(rec, pinRequest(http.MethodPost))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("BFLA: expected 403 for low-privilege caller, got %d", rec.Code)
	}
	if spy.calls != 0 {
		t.Fatal("must not broadcast when the pin write is rejected")
	}
}

func TestMessageHandler_Pin_NotFoundReturns404(t *testing.T) {
	// IDOR: message not in channel (or not visible) → 404, non-enumerating.
	h := pinHandler(&fakePinProvider{pinErr: domain.ErrNotFound}, &spyPinBroadcaster{})
	rec := httptest.NewRecorder()
	h.PinMessage(rec, pinRequest(http.MethodPost))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

func TestMessageHandler_Pin_LimitReachedReturns409(t *testing.T) {
	h := pinHandler(&fakePinProvider{pinErr: domain.ErrPinLimitReached}, &spyPinBroadcaster{})
	rec := httptest.NewRecorder()
	h.PinMessage(rec, pinRequest(http.MethodPost))
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409 at the pin cap, got %d", rec.Code)
	}
}

func TestMessageHandler_Pin_InvalidChannelIDReturns400(t *testing.T) {
	h := pinHandler(&fakePinProvider{}, &spyPinBroadcaster{})
	rec := httptest.NewRecorder()
	r := requestWithUser(http.MethodPost, "/api/chat/channels/bad/messages/"+testMessageID+"/pin", nil)
	r.SetPathValue("channelID", "not-a-uuid")
	r.SetPathValue("messageID", testMessageID)
	h.PinMessage(rec, r)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestMessageHandler_Unpin_Success204AndBroadcastsUnpinned(t *testing.T) {
	spy := &spyPinBroadcaster{}
	h := pinHandler(&fakePinProvider{}, spy)
	rec := httptest.NewRecorder()
	h.UnpinMessage(rec, pinRequest(http.MethodDelete))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", rec.Code)
	}
	if spy.calls != 1 || spy.pinned {
		t.Fatalf("expected one unpinned broadcast, got calls=%d pinned=%v", spy.calls, spy.pinned)
	}
}

func TestMessageHandler_ListPins_ReturnsPinsNewestFirst(t *testing.T) {
	h := pinHandler(&fakePinProvider{listOut: []domain.PinnedMessage{
		{Message: domain.Message{ID: testMessageID, Status: domain.MessageStatusActive}, PinnedByUserID: "user-9"},
	}}, &spyPinBroadcaster{})
	rec := httptest.NewRecorder()
	r := requestWithUser(http.MethodGet, "/api/chat/channels/"+testChannelID+"/pins", nil)
	r.SetPathValue("channelID", testChannelID)
	h.ListPins(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if body := rec.Body.String(); !strings.Contains(body, `"pins"`) || !strings.Contains(body, testMessageID) {
		t.Fatalf("expected pins list with the message, got %s", body)
	}
}

func TestMessageHandler_ListPins_NotFoundReturns404(t *testing.T) {
	h := pinHandler(&fakePinProvider{listErr: domain.ErrNotFound}, &spyPinBroadcaster{})
	rec := httptest.NewRecorder()
	r := requestWithUser(http.MethodGet, "/api/chat/channels/"+testChannelID+"/pins", nil)
	r.SetPathValue("channelID", testChannelID)
	h.ListPins(rec, r)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("private channel not visible: expected 404, got %d", rec.Code)
	}
}
