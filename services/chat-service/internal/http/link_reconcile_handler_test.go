package httpapi_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	httpapi "github.com/nicrepository/nchat/services/chat-service/internal/http"
	"github.com/nicrepository/nchat/services/chat-service/internal/service"
)

// The "Verificar novamente" endpoint (issue #135).
//
// The endpoint's whole attack surface is what a client is allowed to name. It
// names a message id in the path and sends an empty body — no URL, no scan uuid,
// no workspace — and everything else is derived server-side. A variant that let a
// client name a URL would be a Cloudflare search proxy anyone with an account
// could use, billed to this deployment, so the tests below are mostly about the
// inputs that do *not* exist.

// fakeLinkReconciler records what the handler passed down and stages what comes
// back.
type fakeLinkReconciler struct {
	ready  bool
	result service.ReconcileMessageResult
	err    error
	last   service.ReconcileMessageInput
	calls  int
}

func (f *fakeLinkReconciler) Ready() bool { return f.ready }

func (f *fakeLinkReconciler) ReconcileMessage(
	_ context.Context, in service.ReconcileMessageInput,
) (service.ReconcileMessageResult, error) {
	f.calls++
	f.last = in
	return f.result, f.err
}

// fakeActionLimiter is the shared deployment-wide limiter, in memory.
//
// The real one is a Valkey counter keyed by a hash of the user id, so the budget
// holds across replicas. What matters to the handler is only its answer, so this
// stages that — including the failure, which must be a refusal and not a pass.
type fakeActionLimiter struct {
	allow  bool
	err    error
	calls  int
	action string
	limit  int
	window int
}

func (f *fakeActionLimiter) AllowActionWithLimit(
	_ context.Context, _, action string, maxActions, windowSeconds int,
) (bool, error) {
	f.calls++
	f.action, f.limit, f.window = action, maxActions, windowSeconds
	return f.allow, f.err
}

func reconcileHandler(t *testing.T, reconciler *fakeLinkReconciler) *httpapi.MessageHandler {
	t.Helper()
	return reconcileHandlerWithLimiter(t, reconciler, &fakeActionLimiter{allow: true})
}

func reconcileHandlerWithLimiter(
	t *testing.T, reconciler *fakeLinkReconciler, limiter *fakeActionLimiter,
) *httpapi.MessageHandler {
	t.Helper()
	return httpapi.NewMessageHandler(
		&fakeWorkspaceResolver{workspace: activeWorkspace()}, &fakeMessageProvider{}, nil,
	).WithLinkReconcile(reconciler, limiter)
}

// reconcileRequest builds the request the router would produce, including the
// path value the mux extracts.
func reconcileRequest(messageID string, authenticated bool) *http.Request {
	target := "/api/chat/messages/" + messageID + "/link-safety/reconcile"
	request := httptest.NewRequest(http.MethodPost, target, nil)
	request.SetPathValue("messageID", messageID)
	if authenticated {
		ctx := context.WithValue(request.Context(), httpapi.ExportCtxKeyUserID, msgTestUserID)
		request = request.WithContext(ctx)
	}
	return request
}

func TestReconcileLinkSafety_AnswersTheStateAndARetryHint(t *testing.T) {
	updatedAt := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	reconciler := &fakeLinkReconciler{
		ready: true,
		result: service.ReconcileMessageResult{
			State: domain.MessageLinkSafetySafe, UpdatedAt: updatedAt, RetryAfter: time.Minute,
		},
	}
	handler := reconcileHandler(t, reconciler)
	recorder := httptest.NewRecorder()

	handler.ReconcileMessageLinkSafety(recorder, reconcileRequest(testMessageID, true))

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", recorder.Code, recorder.Body.String())
	}
	data := decodeBody(t, recorder)["data"].(map[string]any)
	if data["link_safety_state"] != "safe" {
		t.Fatalf("state = %v", data["link_safety_state"])
	}
	if data["retry_after_seconds"].(float64) != 60 {
		t.Fatalf("retry hint = %v", data["retry_after_seconds"])
	}
	if data["updated_at"] != updatedAt.Format(time.RFC3339) {
		t.Fatalf("updated_at = %v", data["updated_at"])
	}

	// The identity and the workspace come from the session, never from the
	// request, and the message id is the only thing the client chose.
	if reconciler.last.ViewerID != msgTestUserID {
		t.Fatalf("viewer = %q, want the authenticated user", reconciler.last.ViewerID)
	}
	if reconciler.last.WorkspaceID == "" {
		t.Fatal("the workspace was not resolved server-side")
	}
	if reconciler.last.MessageID != testMessageID {
		t.Fatalf("message = %q", reconciler.last.MessageID)
	}

	// Nothing about the provider, the URLs or the scan reaches the caller — and in
	// particular nothing says a scan was started, because none ever is.
	body := strings.ToLower(recorder.Body.String())
	for _, forbidden := range []string{
		"http://", "https://", "canonical", "scan_uuid", "uuid", "cloudflare",
		"verdict", "query", "refusing", "hostname", "account",
	} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("the response carries %q: %s", forbidden, recorder.Body.String())
		}
	}
}

// Nothing new learned still answers 200 with the current state. A refusal would
// teach a client to retry harder, which is the opposite of what this endpoint
// wants.
func TestReconcileLinkSafety_AnswersWhenNothingChanged(t *testing.T) {
	reconciler := &fakeLinkReconciler{
		ready: true,
		result: service.ReconcileMessageResult{
			State:      domain.MessageLinkSafetyInconclusive,
			UpdatedAt:  time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC),
			RetryAfter: time.Minute,
		},
	}
	handler := reconcileHandler(t, reconciler)
	recorder := httptest.NewRecorder()

	handler.ReconcileMessageLinkSafety(recorder, reconcileRequest(testMessageID, true))

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", recorder.Code, recorder.Body.String())
	}
	data := decodeBody(t, recorder)["data"].(map[string]any)
	if data["link_safety_state"] != "inconclusive" {
		t.Fatalf("state = %v", data["link_safety_state"])
	}
}

func TestReconcileLinkSafety_RequiresAuthentication(t *testing.T) {
	reconciler := &fakeLinkReconciler{ready: true}
	handler := reconcileHandler(t, reconciler)
	recorder := httptest.NewRecorder()

	handler.ReconcileMessageLinkSafety(recorder, reconcileRequest(testMessageID, false))

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d: %s", recorder.Code, recorder.Body.String())
	}
	if reconciler.calls != 0 {
		t.Fatal("an unauthenticated request reached the service")
	}
}

// A message id that is not a UUID never reaches the service, so it can never
// reach a query.
func TestReconcileLinkSafety_RejectsAMalformedMessageID(t *testing.T) {
	reconciler := &fakeLinkReconciler{ready: true}
	handler := reconcileHandler(t, reconciler)
	recorder := httptest.NewRecorder()

	handler.ReconcileMessageLinkSafety(recorder, reconcileRequest("not-a-uuid", true))

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", recorder.Code, recorder.Body.String())
	}
	if reconciler.calls != 0 {
		t.Fatal("a malformed id reached the service")
	}
}

// A message this caller may not read is 404 and not 403, so the endpoint cannot
// be used to discover which message ids exist.
func TestReconcileLinkSafety_IsNonEnumerating(t *testing.T) {
	reconciler := &fakeLinkReconciler{ready: true, err: domain.ErrNotFound}
	handler := reconcileHandler(t, reconciler)
	recorder := httptest.NewRecorder()

	handler.ReconcileMessageLinkSafety(recorder, reconcileRequest(testMessageID, true))

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", recorder.Code, recorder.Body.String())
	}
}

// A deployment with no provider says so plainly rather than pretending it
// looked: a client told "nothing new" would stop asking.
func TestReconcileLinkSafety_IsUnavailableWithoutAProvider(t *testing.T) {
	for name, handler := range map[string]*httpapi.MessageHandler{
		"never wired": httpapi.NewMessageHandler(
			&fakeWorkspaceResolver{workspace: activeWorkspace()}, &fakeMessageProvider{}, nil),
		"not ready": reconcileHandler(t, &fakeLinkReconciler{ready: false}),
		"no limiter": httpapi.NewMessageHandler(
			&fakeWorkspaceResolver{workspace: activeWorkspace()}, &fakeMessageProvider{}, nil,
		).WithLinkReconcile(&fakeLinkReconciler{ready: true}, nil),
	} {
		t.Run(name, func(t *testing.T) {
			recorder := httptest.NewRecorder()

			handler.ReconcileMessageLinkSafety(recorder, reconcileRequest(testMessageID, true))

			if recorder.Code != http.StatusServiceUnavailable {
				t.Fatalf("expected 503, got %d: %s", recorder.Code, recorder.Body.String())
			}
		})
	}
}

// The route is registered for POST and for nothing else, and it is
// message-scoped: there is deliberately no URL-scoped variant that would let a
// caller ask about an arbitrary address.
func TestReconcileLinkSafety_RouteIsMessageScoped(t *testing.T) {
	if !strings.Contains(httpapi.RouteMessageLinkSafetyReconcile, "{messageID}") {
		t.Fatalf("route %q is not message-scoped", httpapi.RouteMessageLinkSafetyReconcile)
	}
	if strings.Contains(httpapi.RouteMessageLinkSafetyReconcile, "url") {
		t.Fatalf("route %q names a url", httpapi.RouteMessageLinkSafetyReconcile)
	}
}

// The per-user budget is spent before anything reaches the paid third party, so
// exhausting it must refuse the call outright — and say when to come back, since
// a client with no hint retries immediately and spends the next window too.
func TestReconcileLinkSafety_RefusesWhenTheBudgetIsSpent(t *testing.T) {
	reconciler := &fakeLinkReconciler{ready: true}
	limiter := &fakeActionLimiter{allow: false}
	handler := reconcileHandlerWithLimiter(t, reconciler, limiter)
	recorder := httptest.NewRecorder()

	handler.ReconcileMessageLinkSafety(recorder, reconcileRequest(testMessageID, true))

	if recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d: %s", recorder.Code, recorder.Body.String())
	}
	if recorder.Header().Get("Retry-After") == "" {
		t.Fatal("a refused call carried no Retry-After")
	}
	if reconciler.calls != 0 {
		t.Fatal("a rate-limited request still reached the provider")
	}
}

// A limiter that cannot answer is a refusal, not a pass. Not knowing whether
// there is allowance left is not permission to spend it — otherwise a Valkey
// outage turns the deployment-wide budget off entirely, which is exactly when
// an attacker would want it off.
func TestReconcileLinkSafety_TreatsAnUnreachableLimiterAsARefusal(t *testing.T) {
	reconciler := &fakeLinkReconciler{ready: true}
	limiter := &fakeActionLimiter{allow: true, err: errors.New("valkey unavailable")}
	handler := reconcileHandlerWithLimiter(t, reconciler, limiter)
	recorder := httptest.NewRecorder()

	handler.ReconcileMessageLinkSafety(recorder, reconcileRequest(testMessageID, true))

	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d: %s", recorder.Code, recorder.Body.String())
	}
	// The staged limiter would have said "allow" if its error were ignored, so
	// this also proves the error is what refused the call.
	if reconciler.calls != 0 {
		t.Fatal("an unanswerable limiter let the request through to the provider")
	}
}
