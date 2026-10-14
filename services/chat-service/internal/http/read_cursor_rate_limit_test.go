package httpapi_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
)

// Issue #1082, security review SR-002: POST …/read is written by the read
// cursor writer, at most one write per 400ms window plus a round trip while a
// reader scrolls. It has a budget of its own, sized from that cadence, and no
// longer shares the ten-a-minute budget of pin, mute and ownership actions.

const (
	readRateChannel = "11111111-1111-4111-8111-111111111111"
	readRateDM      = "22222222-2222-4222-8222-222222222222"
	readRateMessage = "33333333-3333-4333-8333-333333333333"
)

// readingAndPinningProvider answers both the read and the pin routes.
type readingAndPinningProvider struct {
	stubSidebarProvider
	reads, pins int
}

func (s *readingAndPinningProvider) MarkConversationRead(context.Context, string, string, string, *string) (domain.ConversationReadState, error) {
	s.reads++
	return domain.ConversationReadState{}, nil
}

func (s *readingAndPinningProvider) PinConversation(context.Context, string, string, string) error {
	s.pins++
	return nil
}

func (s *readingAndPinningProvider) UnpinConversation(context.Context, string, string, string) error {
	return nil
}

type readRateEnv struct {
	t      *testing.T
	router http.Handler
	svc    *readingAndPinningProvider
}

func newReadRateEnv(t *testing.T) *readRateEnv {
	t.Helper()
	svc := &readingAndPinningProvider{}
	return &readRateEnv{t: t, router: sidebarRouter(makeTestValidator(t), svc), svc: svc}
}

// do sends one request as `user` (no token when empty) and returns its status.
func (e *readRateEnv) do(method, path, user, body string) int {
	e.t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if user != "" {
		setBearerToken(req, makeTestToken(e.t, user, testHMACSecret, testIssuer, testAudience, time.Hour))
	}
	recorder := httptest.NewRecorder()
	e.router.ServeHTTP(recorder, req)
	return recorder.Code
}

// read writes the cursor of `user` through the channel route.
func (e *readRateEnv) read(user string) int {
	e.t.Helper()
	body := `{"last_read_message_id":"` + readRateMessage + `","read_cursor":"message"}`
	return e.do(http.MethodPost, "/api/chat/channels/"+readRateChannel+"/read", user, body)
}

func (e *readRateEnv) pin(user string) int {
	e.t.Helper()
	return e.do(http.MethodPost, "/api/chat/channels/"+readRateChannel+"/sidebar-pin", user, "")
}

// admitted counts how many of n reads by `user` are let through.
func (e *readRateEnv) admitted(user string, n int) int {
	e.t.Helper()
	count := 0
	for range n {
		if e.read(user) == http.StatusOK {
			count++
		}
	}
	return count
}

// Twenty seconds of continuous reading at the writer's cadence with a 100ms
// round trip: one write every 500ms, forty in all.
func TestReadCursorRateLimit_AdmitsTwentySecondsOfContinuousReading(t *testing.T) {
	env := newReadRateEnv(t)
	if got := env.admitted(testUserID, 40); got != 40 {
		t.Fatalf("admitted %d of 40 legitimate reads", got)
	}
}

// A full minute at the writer's fastest possible cadence — one write per
// 400ms window, with an instant network — still fits.
func TestReadCursorRateLimit_AdmitsAMinuteAtTheWritersFastestCadence(t *testing.T) {
	env := newReadRateEnv(t)
	if got := env.admitted(testUserID, 150); got != 150 {
		t.Fatalf("admitted %d of 150", got)
	}
}

func TestReadCursorRateLimit_DoesNotSpendThePinBudget(t *testing.T) {
	env := newReadRateEnv(t)
	env.admitted(testUserID, 40)
	if status := env.pin(testUserID); status != http.StatusNoContent {
		t.Fatalf("pin after forty reads = %d, want 204", status)
	}
}

func TestReadCursorRateLimit_PinActionsDoNotSpendTheReadBudget(t *testing.T) {
	env := newReadRateEnv(t)
	for range 12 {
		env.pin(testUserID)
	}
	if status := env.read(testUserID); status != http.StatusOK {
		t.Fatalf("read after the pin budget ran out = %d, want 200", status)
	}
}

func TestReadCursorRateLimit_StillLimitsAFlood(t *testing.T) {
	env := newReadRateEnv(t)
	env.admitted(testUserID, 180)
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/chat/channels/"+readRateChannel+"/read", nil)
	setBearerToken(req, makeTestToken(t, testUserID, testHMACSecret, testIssuer, testAudience, time.Hour))
	env.router.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusTooManyRequests || recorder.Header().Get("Retry-After") != "60" {
		t.Fatalf("read 181 = %d (Retry-After %q), want 429 with Retry-After 60",
			recorder.Code, recorder.Header().Get("Retry-After"))
	}
	if env.svc.reads != 180 {
		t.Fatalf("service saw %d reads, want 180 — a refused read must not reach it", env.svc.reads)
	}
}

func TestReadCursorRateLimit_IsPerUser(t *testing.T) {
	env := newReadRateEnv(t)
	env.admitted(testUserID, 181)
	if status := env.read("another-user-456"); status != http.StatusOK {
		t.Fatalf("another user's read = %d, want 200", status)
	}
}

func TestReadCursorRateLimit_StillRequiresAuthentication(t *testing.T) {
	env := newReadRateEnv(t)
	if status := env.read(""); status != http.StatusUnauthorized {
		t.Fatalf("anonymous read = %d, want 401", status)
	}
	if status := env.do(http.MethodPost, "/api/chat/dm/"+readRateDM+"/read", "", ""); status != http.StatusUnauthorized {
		t.Fatalf("anonymous DM read = %d, want 401", status)
	}
	if env.svc.reads != 0 {
		t.Fatalf("service saw %d anonymous reads", env.svc.reads)
	}
}

// The budget sits in front of the same handler: a conversation or message the
// caller may not read is still the service's non-enumerating 404, and a
// terminal (keepalive) write is the same request on the wire.
func TestReadCursorRateLimit_KeepsConversationAuthorization(t *testing.T) {
	svc := &readingSidebarProvider{err: domain.ErrNotFound}
	router := sidebarRouter(makeTestValidator(t), svc)
	for _, path := range []string{
		"/api/chat/channels/" + readRateChannel + "/read",
		"/api/chat/dm/" + readRateDM + "/read",
	} {
		req := httptest.NewRequest(http.MethodPost, path, nil)
		setBearerToken(req, makeTestToken(t, testUserID, testHMACSecret, testIssuer, testAudience, time.Hour))
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, req)
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("%s for a conversation outside the caller = %d, want 404", path, recorder.Code)
		}
	}
	if len(svc.args) == 0 || svc.args[0] != testUserID {
		t.Fatalf("service authorized against %v, want the token's user", svc.args)
	}
}
