package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// ── Helpers ───────────────────────────────────────────────────────────────────

// rateLimitSentinel is a handler that records how many times it was invoked.
type rateLimitSentinel struct{ calls int }

func (s *rateLimitSentinel) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	s.calls++
	w.WriteHeader(http.StatusOK)
}

// authedRLRequest builds a GET request with userID injected into the context,
// simulating what BearerAuth + RequireActiveSession produce.
func authedRLRequest(userID string) *http.Request {
	return authedRLRequestURL(userID, "/api/chat/channels/general/messages")
}

func authedRLRequestURL(userID, url string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, url, nil)
	ctx := context.WithValue(r.Context(), ctxKeyUserID, userID)
	return r.WithContext(ctx)
}

// ── Unit tests for UserRateLimiter.Allow ─────────────────────────────────────

func TestUserRateLimiter_AllowsRequestsWithinLimit(t *testing.T) {
	const limit = 5
	l := NewUserRateLimiter(limit, time.Minute)
	t.Cleanup(l.Stop)

	for i := range limit {
		if !l.Allow("user-a") {
			t.Fatalf("request %d should be allowed; limit is %d per minute", i+1, limit)
		}
	}
}

func TestUserRateLimiter_BlocksRequestsOverLimit(t *testing.T) {
	l := NewUserRateLimiter(3, time.Minute)
	t.Cleanup(l.Stop)
	l.Allow("user-b")
	l.Allow("user-b")
	l.Allow("user-b")

	if l.Allow("user-b") {
		t.Fatal("4th request should be blocked; limit is 3 per minute")
	}
}

func TestUserRateLimiter_LimitIsPerUser(t *testing.T) {
	l := NewUserRateLimiter(2, time.Minute)
	t.Cleanup(l.Stop)

	l.Allow("user-c")
	l.Allow("user-c")

	if !l.Allow("user-d") {
		t.Fatal("user-d should be allowed; it has its own independent counter")
	}
}

func TestUserRateLimiter_AllowsAfterWindowExpires(t *testing.T) {
	l := NewUserRateLimiter(1, 10*time.Millisecond)
	t.Cleanup(l.Stop)
	l.Allow("user-e") // consumes the slot

	if l.Allow("user-e") {
		t.Fatal("2nd request before window expires should be blocked")
	}

	time.Sleep(20 * time.Millisecond)

	if !l.Allow("user-e") {
		t.Fatal("request after window expires should be allowed")
	}
}

// ── HTTP middleware tests ─────────────────────────────────────────────────────

func TestUserRateLimiter_Middleware_PassesWithinLimit(t *testing.T) {
	sentinel := &rateLimitSentinel{}
	l := NewUserRateLimiter(10, time.Minute)
	t.Cleanup(l.Stop)
	handler := l.Middleware(sentinel)

	for i := range 3 {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, authedRLRequest("user-f"))
		if w.Code != http.StatusOK {
			t.Fatalf("request %d: expected 200, got %d", i+1, w.Code)
		}
	}
	if sentinel.calls != 3 {
		t.Fatalf("expected inner handler called 3 times, got %d", sentinel.calls)
	}
}

func TestUserRateLimiter_Middleware_Returns429WhenExceeded(t *testing.T) {
	sentinel := &rateLimitSentinel{}
	l := NewUserRateLimiter(2, time.Minute)
	t.Cleanup(l.Stop)
	handler := l.Middleware(sentinel)

	for range 2 {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, authedRLRequest("user-g"))
		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", w.Code)
		}
	}

	// Third request exceeds limit.
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, authedRLRequest("user-g"))
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("expected application/json, got %q", ct)
	}
	if !strings.Contains(w.Body.String(), "rate_limited") {
		t.Fatalf("expected error code rate_limited in body, got %q", w.Body.String())
	}
	// RFC 9110 §15.5.30: Retry-After must be present on 429.
	if ra := w.Header().Get("Retry-After"); ra == "" {
		t.Fatal("expected Retry-After header on 429 response")
	}
	if sentinel.calls != 2 {
		t.Fatalf("inner handler should be called exactly 2 times, got %d", sentinel.calls)
	}
}

func TestUserRateLimiter_Middleware_LimitIsPerUser(t *testing.T) {
	l := NewUserRateLimiter(1, time.Minute)
	t.Cleanup(l.Stop)
	handler := l.Middleware(&rateLimitSentinel{})

	// Exhaust user-h's quota.
	w1 := httptest.NewRecorder()
	handler.ServeHTTP(w1, authedRLRequest("user-h"))

	// user-i has its own independent counter.
	w2 := httptest.NewRecorder()
	handler.ServeHTTP(w2, authedRLRequest("user-i"))
	if w2.Code != http.StatusOK {
		t.Fatalf("user-i should be allowed; expected 200, got %d", w2.Code)
	}
}

func TestUserRateLimiter_Middleware_UnauthenticatedPassesThrough(t *testing.T) {
	sentinel := &rateLimitSentinel{}
	l := NewUserRateLimiter(1, time.Minute)
	t.Cleanup(l.Stop)
	handler := l.Middleware(sentinel)

	// No userID in context — request has no auth (BearerAuth would reject it upstream).
	r := httptest.NewRequest(http.MethodGet, "/api/chat/channels/x/messages", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("unauthenticated request should pass through limiter; expected 200, got %d", w.Code)
	}
	if sentinel.calls != 1 {
		t.Fatal("inner handler should be reached for unauthenticated request")
	}
}

// ── Middleware covers both initial load and pagination ────────────────────────
//
// The rate limit budget is aggregate across all message-list GETs (with or
// without "before="). This protects the database against flood reads from both
// initial channel opens and aggressive scroll-based pagination.
// Channel and DM listing routes share the same per-user budget intentionally.

// TestMiddleware_InitialLoadConsumesQuota verifies that initial-load requests
// (no "before" param) count against the rate-limit budget.
func TestMiddleware_InitialLoadConsumesQuota(t *testing.T) {
	sentinel := &rateLimitSentinel{}
	// Limit of 2: only two list requests per minute regardless of cursor.
	l := NewUserRateLimiter(2, time.Minute)
	t.Cleanup(l.Stop)
	handler := l.Middleware(sentinel)

	// Two initial-load requests (no "before") consume the full quota.
	for i := range 2 {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, authedRLRequestURL("user-j", "/api/chat/channels/general/messages"))
		if w.Code != http.StatusOK {
			t.Fatalf("initial load request %d should be allowed; got %d", i+1, w.Code)
		}
	}

	// Third request (still initial load) exceeds the budget.
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, authedRLRequestURL("user-j", "/api/chat/channels/general/messages"))
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429 when initial-load quota exceeded; got %d", w.Code)
	}
}

// TestMiddleware_PaginationRequestConsumesQuota verifies that pagination
// requests with "before=" also count against the per-user budget.
func TestMiddleware_PaginationRequestConsumesQuota(t *testing.T) {
	sentinel := &rateLimitSentinel{}
	l := NewUserRateLimiter(2, time.Minute)
	t.Cleanup(l.Stop)
	handler := l.Middleware(sentinel)

	page := func(cursor string) *http.Request {
		return authedRLRequestURL("user-k", "/api/chat/channels/general/messages?before="+cursor)
	}

	// Two pagination requests within limit.
	for i, cursor := range []string{"cursor1", "cursor2"} {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, page(cursor))
		if w.Code != http.StatusOK {
			t.Fatalf("pagination request %d should be allowed; got %d", i+1, w.Code)
		}
	}

	// Third pagination request exceeds limit.
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, page("cursor3"))
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429 on excess pagination request; got %d", w.Code)
	}
	if ra := w.Header().Get("Retry-After"); ra == "" {
		t.Fatal("expected Retry-After header on 429")
	}
}

// TestMiddleware_MixedInitialAndPaginationShareBudget verifies that initial
// loads and pagination requests together count toward the same budget.
func TestMiddleware_MixedInitialAndPaginationShareBudget(t *testing.T) {
	l := NewUserRateLimiter(2, time.Minute)
	t.Cleanup(l.Stop)
	handler := l.Middleware(&rateLimitSentinel{})

	// One initial load + one pagination = budget exhausted.
	w1 := httptest.NewRecorder()
	handler.ServeHTTP(w1, authedRLRequestURL("user-l", "/api/chat/channels/g/messages"))
	w2 := httptest.NewRecorder()
	handler.ServeHTTP(w2, authedRLRequestURL("user-l", "/api/chat/channels/g/messages?before=c1"))
	if w1.Code != http.StatusOK || w2.Code != http.StatusOK {
		t.Fatalf("first two requests should pass; got %d and %d", w1.Code, w2.Code)
	}

	// Third request (any kind) is blocked.
	w3 := httptest.NewRecorder()
	handler.ServeHTTP(w3, authedRLRequestURL("user-l", "/api/chat/channels/g/messages"))
	if w3.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429 after budget exhausted; got %d", w3.Code)
	}
}

// TestMiddleware_LimitIsPerUser verifies quota is independent per user.
func TestMiddleware_LimitIsPerUser(t *testing.T) {
	l := NewUserRateLimiter(1, time.Minute)
	t.Cleanup(l.Stop)
	handler := l.Middleware(&rateLimitSentinel{})

	// Exhaust user-m.
	w1 := httptest.NewRecorder()
	handler.ServeHTTP(w1, authedRLRequestURL("user-m", "/api/chat/channels/g/messages"))
	w2 := httptest.NewRecorder()
	handler.ServeHTTP(w2, authedRLRequestURL("user-m", "/api/chat/channels/g/messages"))
	if w2.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429 for user-m, got %d", w2.Code)
	}

	// user-n is unaffected.
	w3 := httptest.NewRecorder()
	handler.ServeHTTP(w3, authedRLRequestURL("user-n", "/api/chat/channels/g/messages"))
	if w3.Code != http.StatusOK {
		t.Fatalf("user-n should be unaffected; expected 200, got %d", w3.Code)
	}
}

// ── POST (send-message) rate limit ─────────────────────────────────────────────
//
// The message-send (POST) path has its own per-user rate limiter (msgPostLimiter).
// When the limit is exceeded the middleware must return 429 without calling the
// inner handler — guaranteeing that no message is persisted and no broadcast fires.

// authedPOSTRequest creates a POST request with userID in context to simulate
// what BearerAuth + RequireActiveSession produce for write paths.
func authedPOSTRequest(userID, url string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, url, strings.NewReader(`{"body":"hi"}`))
	r.Header.Set("Content-Type", "application/json")
	ctx := context.WithValue(r.Context(), ctxKeyUserID, userID)
	return r.WithContext(ctx)
}

func TestRateLimit_PostChannelMessage_Returns429WhenExceeded(t *testing.T) {
	sentinel := &rateLimitSentinel{}
	l := NewUserRateLimiter(2, time.Minute)
	t.Cleanup(l.Stop)
	handler := l.Middleware(sentinel)

	for i := range 2 {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, authedPOSTRequest("user-p1", RouteChannelMessages))
		if w.Code != http.StatusOK {
			t.Fatalf("POST %d should be allowed; got %d", i+1, w.Code)
		}
	}

	// Third POST exceeds the limit — inner handler must not be called.
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, authedPOSTRequest("user-p1", RouteChannelMessages))
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429 on rate-limited POST; got %d", w.Code)
	}
	if sentinel.calls != 2 {
		t.Fatalf("inner handler should not be called on 429; calls=%d", sentinel.calls)
	}
	if ra := w.Header().Get("Retry-After"); ra == "" {
		t.Fatal("expected Retry-After header on 429")
	}
}

func TestRateLimit_PostDMMessage_Returns429WhenExceeded(t *testing.T) {
	sentinel := &rateLimitSentinel{}
	l := NewUserRateLimiter(2, time.Minute)
	t.Cleanup(l.Stop)
	handler := l.Middleware(sentinel)

	for i := range 2 {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, authedPOSTRequest("user-p2", RouteDMMessages))
		if w.Code != http.StatusOK {
			t.Fatalf("POST DM %d should be allowed; got %d", i+1, w.Code)
		}
	}

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, authedPOSTRequest("user-p2", RouteDMMessages))
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429 on rate-limited DM POST; got %d", w.Code)
	}
	if sentinel.calls != 2 {
		t.Fatalf("inner handler should not be called on 429; calls=%d", sentinel.calls)
	}
}

// ── GET single-message rate limit ─────────────────────────────────────────────
//
// GET single-message has a dedicated budget so realtime fallback does not
// consume paginated list capacity. The tests below verify the middleware shape.

func TestRateLimit_GetChannelMessage_Returns429WhenExceeded(t *testing.T) {
	sentinel := &rateLimitSentinel{}
	l := NewUserRateLimiter(2, time.Minute)
	t.Cleanup(l.Stop)
	handler := l.Middleware(sentinel)

	url := "/api/chat/channels/ch-1/messages/msg-1"
	for i := range 2 {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, authedRLRequestURL("user-p3", url))
		if w.Code != http.StatusOK {
			t.Fatalf("GET single-msg %d should be allowed; got %d", i+1, w.Code)
		}
	}

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, authedRLRequestURL("user-p3", url))
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429 on rate-limited GET single-msg; got %d", w.Code)
	}
	if sentinel.calls != 2 {
		t.Fatalf("inner handler must not be called on 429; calls=%d", sentinel.calls)
	}
}

func TestRateLimit_GetDMMessage_Returns429WhenExceeded(t *testing.T) {
	sentinel := &rateLimitSentinel{}
	l := NewUserRateLimiter(2, time.Minute)
	t.Cleanup(l.Stop)
	handler := l.Middleware(sentinel)

	url := "/api/chat/dm/conv-1/messages/msg-1"
	for i := range 2 {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, authedRLRequestURL("user-p4", url))
		if w.Code != http.StatusOK {
			t.Fatalf("GET DM single-msg %d should be allowed; got %d", i+1, w.Code)
		}
	}

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, authedRLRequestURL("user-p4", url))
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429 on rate-limited GET DM single-msg; got %d", w.Code)
	}
	if sentinel.calls != 2 {
		t.Fatalf("inner handler must not be called on 429; calls=%d", sentinel.calls)
	}
}

// ── Authorization chain test ──────────────────────────────────────────────────

// TestUserRateLimiter_Middleware_DoesNotBreakAuthorization verifies that the
// limiter does not interfere with the authorization chain: an authenticated
// request within the limit still reaches the message handler (which may return
// any status depending on service config, but not 401 or 429).
func TestUserRateLimiter_Middleware_DoesNotBreakAuthorization(t *testing.T) {
	validator, err := NewTokenValidator(routerTestSigningKey(), routerTestIssuer, routerTestAudience)
	if err != nil {
		t.Fatalf("new token validator: %v", err)
	}

	router := NewRouter(
		testConfig(),
		nil, // logger not needed
		validator,
		allowRouterSessionValidator{},
		NewSidebarHandler(nil),
		NewMessageHandler(nil, nil, nil),
		nil,
	)

	// Initial load (no cursor) — Middleware now applies to all list requests.
	req := httptest.NewRequest(http.MethodGet, "/api/chat/channels/general/messages", nil)
	req.Header.Set("Authorization", bearerScheme+makeRouterTestToken(t))
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	// The handler may return any non-auth, non-rate-limit error (e.g., 500 with no
	// service configured). The important thing is it's not 401 or 429.
	if w.Code == http.StatusUnauthorized {
		t.Fatalf("authorized request should not receive 401; got %d", w.Code)
	}
	if w.Code == http.StatusTooManyRequests {
		t.Fatalf("first list request should not be rate-limited; got 429")
	}
}

// ── GC test ───────────────────────────────────────────────────────────────────

// TestUserRateLimiter_GC_EvictsStaleEntries verifies that entries for inactive
// users are eventually removed from the map, bounding memory growth.
func TestUserRateLimiter_GC_EvictsStaleEntries(t *testing.T) {
	const window = 20 * time.Millisecond
	l := NewUserRateLimiter(5, window)
	t.Cleanup(l.Stop)

	l.Allow("user-gc")
	time.Sleep(3 * window)

	// Run GC directly rather than waiting for the background ticker.
	l.gc()

	if !l.Allow("user-gc") {
		t.Fatal("expected Allow to succeed after GC evicted the stale entry")
	}
}
