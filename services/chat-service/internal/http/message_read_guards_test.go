package httpapi_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	httpapi "github.com/nicrepository/nchat/services/chat-service/internal/http"
)

// The six read endpoints below each open with the same guard sequence — target
// id, authentication, then the service — written out once per handler rather
// than shared. Their happy paths and their headline behaviours are covered
// elsewhere; what was never exercised is the guards themselves on most of them,
// which is the half that decides whether an unauthenticated or malformed
// request reaches the service at all.
//
// Each case asserts the status *and* that the service was not consulted. The
// status alone would pass even if the handler asked the service first and threw
// the answer away, which for these endpoints means a read performed on behalf
// of a caller that was never identified.

type messageReadEndpoint struct {
	name string
	// targetParam names the path value this endpoint reads, so a handler that
	// reads the wrong one shows up as a validation failure rather than a
	// silently empty target.
	targetParam string
	// newRequest builds a request for the endpoint with the given target id,
	// authenticated or not.
	newRequest func(targetID string, authenticated bool) *http.Request
	call       func(*httpapi.MessageHandler, *httptest.ResponseRecorder, *http.Request)
	// callCount reports how many times the endpoint's service method was
	// invoked. Deliberately a count read off the fake rather than anything
	// inferred from the captured input: an unauthenticated request that *did*
	// reach the service would carry CallerID "", which is exactly what a
	// never-called fake also holds, so argument inspection cannot tell the two
	// apart. The count can.
	callCount func(*fakeMessageProvider) int
	// failService arms the provider so the endpoint's service call fails.
	failService func(*fakeMessageProvider, error)
}

func unauthenticatedRequest(method, target string, body io.Reader) *http.Request {
	return httptest.NewRequest(method, target, body)
}

func messageReadEndpoints() []messageReadEndpoint {
	channelTarget := func(method, suffix string, body func() io.Reader) func(string, bool) *http.Request {
		return func(targetID string, authenticated bool) *http.Request {
			url := "/api/chat/channels/" + targetID + suffix
			var r *http.Request
			if authenticated {
				r = requestWithUser(method, url, body())
			} else {
				r = unauthenticatedRequest(method, url, body())
			}
			r.SetPathValue("channelID", targetID)
			r.SetPathValue("messageID", testMessageID)
			return r
		}
	}
	dmTarget := func(method, suffix string, body func() io.Reader) func(string, bool) *http.Request {
		return func(targetID string, authenticated bool) *http.Request {
			url := "/api/chat/dm/" + targetID + suffix
			var r *http.Request
			if authenticated {
				r = requestWithUser(method, url, body())
			} else {
				r = unauthenticatedRequest(method, url, body())
			}
			r.SetPathValue("conversationID", targetID)
			r.SetPathValue("messageID", testMessageID)
			return r
		}
	}
	noBody := func() io.Reader { return nil }
	idsBody := func() io.Reader {
		return strings.NewReader(`{"message_ids":["` + testMessageID + `"]}`)
	}

	return []messageReadEndpoint{
		{
			name:        "GetChannelMessage",
			targetParam: "channel_id",
			newRequest:  channelTarget(http.MethodGet, "/messages/"+testMessageID, noBody),
			call: func(h *httpapi.MessageHandler, rec *httptest.ResponseRecorder, r *http.Request) {
				h.GetChannelMessage(rec, r)
			},
			callCount:   func(f *fakeMessageProvider) int { return f.getChannelMessageCalls },
			failService: func(f *fakeMessageProvider, err error) { f.channelMsgErr = err },
		},
		{
			name:        "GetDMMessage",
			targetParam: "conversation_id",
			newRequest:  dmTarget(http.MethodGet, "/messages/"+testMessageID, noBody),
			call: func(h *httpapi.MessageHandler, rec *httptest.ResponseRecorder, r *http.Request) {
				h.GetDMMessage(rec, r)
			},
			callCount:   func(f *fakeMessageProvider) int { return f.getDMMessageCalls },
			failService: func(f *fakeMessageProvider, err error) { f.dmMsgErr = err },
		},
		{
			name:        "ResolveChannelMessageReferences",
			targetParam: "channel_id",
			newRequest:  channelTarget(http.MethodPost, "/message-references", idsBody),
			call: func(h *httpapi.MessageHandler, rec *httptest.ResponseRecorder, r *http.Request) {
				h.ResolveChannelMessageReferences(rec, r)
			},
			callCount:   func(f *fakeMessageProvider) int { return f.resolveReferencesCalls },
			failService: func(f *fakeMessageProvider, err error) { f.referenceErr = err },
		},
		{
			name:        "ResolveDMMessageReferences",
			targetParam: "conversation_id",
			newRequest:  dmTarget(http.MethodPost, "/message-references", idsBody),
			call: func(h *httpapi.MessageHandler, rec *httptest.ResponseRecorder, r *http.Request) {
				h.ResolveDMMessageReferences(rec, r)
			},
			callCount:   func(f *fakeMessageProvider) int { return f.resolveReferencesCalls },
			failService: func(f *fakeMessageProvider, err error) { f.referenceErr = err },
		},
		{
			name:        "GetChannelMessageSecuritySnapshots",
			targetParam: "channel_id",
			newRequest:  channelTarget(http.MethodPost, "/message-security", idsBody),
			call: func(h *httpapi.MessageHandler, rec *httptest.ResponseRecorder, r *http.Request) {
				h.GetChannelMessageSecuritySnapshots(rec, r)
			},
			callCount:   func(f *fakeMessageProvider) int { return f.securitySnapshotsCalls },
			failService: func(f *fakeMessageProvider, err error) { f.securitySnapshotErr = err },
		},
		{
			name:        "GetDMMessageSecuritySnapshots",
			targetParam: "conversation_id",
			newRequest:  dmTarget(http.MethodPost, "/message-security", idsBody),
			call: func(h *httpapi.MessageHandler, rec *httptest.ResponseRecorder, r *http.Request) {
				h.GetDMMessageSecuritySnapshots(rec, r)
			},
			callCount:   func(f *fakeMessageProvider) int { return f.securitySnapshotsCalls },
			failService: func(f *fakeMessageProvider, err error) { f.securitySnapshotErr = err },
		},
	}
}

// An unauthenticated request must be refused by the handler itself. These
// endpoints all read conversation content, so consulting the service with no
// caller identity is the failure worth guarding, not the status code.
func TestMessageHandler_MessageReads_UnauthenticatedDoNotReachTheService(t *testing.T) {
	for _, endpoint := range messageReadEndpoints() {
		t.Run(endpoint.name, func(t *testing.T) {
			provider := &fakeMessageProvider{}
			h := makeHandlerWithUser(&fakeWorkspaceResolver{workspace: activeWorkspace()}, provider)
			rec := httptest.NewRecorder()

			endpoint.call(h, rec, endpoint.newRequest(validTargetIDFor(endpoint), false))

			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("expected 401, got %d: %s", rec.Code, rec.Body.String())
			}
			if got := endpoint.callCount(provider); got != 0 {
				t.Fatalf("an unauthenticated request reached the service %d time(s); want 0", got)
			}
		})
	}
}

// Positive control for the counters the two guard tests above rely on. Without
// it a counter that never incremented would make both of them pass vacuously —
// the same class of false negative as inferring the call from CallerID.
//
// It is also a real assertion in its own right: exactly one call, so an endpoint
// that asked its service twice for one request would be caught here.
func TestMessageHandler_MessageReads_AuthorizedRequestCallsTheServiceExactlyOnce(t *testing.T) {
	for _, endpoint := range messageReadEndpoints() {
		t.Run(endpoint.name, func(t *testing.T) {
			provider := &fakeMessageProvider{}
			h := makeHandlerWithUser(&fakeWorkspaceResolver{workspace: activeWorkspace()}, provider)
			rec := httptest.NewRecorder()

			endpoint.call(h, rec, endpoint.newRequest(validTargetIDFor(endpoint), true))

			if rec.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
			}
			if got := endpoint.callCount(provider); got != 1 {
				t.Fatalf("service called %d time(s); want exactly 1", got)
			}
		})
	}
}

// A target id that is not a UUID is refused before the service is asked, so a
// malformed path cannot be used to probe the service with an arbitrary string.
func TestMessageHandler_MessageReads_MalformedTargetIDDoesNotReachTheService(t *testing.T) {
	for _, endpoint := range messageReadEndpoints() {
		t.Run(endpoint.name, func(t *testing.T) {
			provider := &fakeMessageProvider{}
			h := makeHandlerWithUser(&fakeWorkspaceResolver{workspace: activeWorkspace()}, provider)
			rec := httptest.NewRecorder()

			endpoint.call(h, rec, endpoint.newRequest("not-a-uuid", true))

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), endpoint.targetParam) {
				t.Fatalf("error does not name %s: %s", endpoint.targetParam, rec.Body.String())
			}
			if got := endpoint.callCount(provider); got != 0 {
				t.Fatalf("a malformed target id reached the service %d time(s); want 0", got)
			}
		})
	}
}

// A refusal from the service must reach the client as the refusal it is. An
// inaccessible conversation is reported as 404 by every one of these endpoints,
// so none of them can be used to discover that a target exists.
func TestMessageHandler_MessageReads_ServiceRefusalIsNotEnumerable(t *testing.T) {
	for _, endpoint := range messageReadEndpoints() {
		t.Run(endpoint.name, func(t *testing.T) {
			provider := &fakeMessageProvider{}
			endpoint.failService(provider, domain.ErrNotFound)
			h := makeHandlerWithUser(&fakeWorkspaceResolver{workspace: activeWorkspace()}, provider)
			rec := httptest.NewRecorder()

			endpoint.call(h, rec, endpoint.newRequest(validTargetIDFor(endpoint), true))

			if rec.Code != http.StatusNotFound {
				t.Fatalf("expected 404, got %d: %s", rec.Code, rec.Body.String())
			}
			body := strings.ToLower(rec.Body.String())
			for _, leak := range []string{"private", "forbidden", "not a member", "permission"} {
				if strings.Contains(body, leak) {
					t.Fatalf("response distinguishes the reason for refusal (%q): %s", leak, rec.Body.String())
				}
			}
		})
	}
}

func validTargetIDFor(endpoint messageReadEndpoint) string {
	if endpoint.targetParam == "conversation_id" {
		return testConversationID
	}
	return testChannelID
}
