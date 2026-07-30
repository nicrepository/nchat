package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	platformlog "github.com/nicrepository/nchat/libs/go/platform/log"
	"github.com/nicrepository/nchat/services/auth-service/internal/domain"
	"github.com/nicrepository/nchat/services/auth-service/internal/storage"
)

// The bootstrap credential can mint an invite conferring workspace ownership,
// so the number of guesses an attacker gets has to be bounded — and bounded
// *before* the comparison, or the bound does not exist. These tests pin the
// ordering, the shared budget, and that a limiter failure refuses rather than
// falls through.

// countingRecorder is a shared budget: one counter per key, so two routers
// built over the same instance model two replicas over one database.
type countingRecorder struct {
	mu      sync.Mutex
	counts  map[string]int
	err     error
	calls   int
	lastKey string
}

func newCountingRecorder() *countingRecorder {
	return &countingRecorder{counts: make(map[string]int)}
}

func (r *countingRecorder) RecordAttempt(_ context.Context, key string, limit int, _ time.Duration) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	r.lastKey = key
	if r.err != nil {
		return false, r.err
	}
	r.counts[key]++
	return r.counts[key] <= limit, nil
}

// Allow lets the same fake stand in for the shared store the router takes.
// Namespaced keys are counted separately, which is what the per-namespace
// isolation tests rely on.
func (r *countingRecorder) Allow(_ context.Context, req storage.DistributedRateLimitRequest) (storage.DistributedRateLimitResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	if r.err != nil {
		return storage.DistributedRateLimitResult{}, r.err
	}
	key := req.Subject
	if req.Namespace != "" {
		key = req.Namespace + ":" + req.Subject
	}
	r.lastKey = key
	r.counts[key]++
	if r.counts[key] <= req.Limit {
		return storage.DistributedRateLimitResult{Allowed: true}, nil
	}
	return storage.DistributedRateLimitResult{RetryAfter: req.Window}, nil
}

func (r *countingRecorder) attempts(key string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.counts[key]
}

// guardProbe records whether the credential comparison and the handler were
// reached, which is the whole question these tests ask.
type guardProbe struct {
	guardCalls   int
	handlerCalls int
}

func bootstrapProbeRouter(t *testing.T, recorder BootstrapAttemptRecorder, probe *guardProbe) http.Handler {
	t.Helper()
	cfg := testConfig()
	cfg.AdminBootstrapToken = bootstrapToken

	guard := AdminBootstrapGuard(cfg.AdminBootstrapToken)
	instrumented := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			probe.guardCalls++
			guard(next).ServeHTTP(w, r)
		})
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		probe.handlerCalls++
		w.WriteHeader(http.StatusCreated)
	})

	limit := RateLimitBootstrapAttempts(BootstrapRateLimitConfig{
		Recorder: recorder,
		Attempts: cfg.AuthBootstrapRateLimitAttempts,
		Window:   time.Duration(cfg.AuthBootstrapRateLimitWindowMinutes) * time.Minute,
	})
	mux := http.NewServeMux()
	mux.Handle(RouteAdminInvites, limit(instrumented(handler)))
	return mux
}

func bootstrapAttemptRequest(token, remoteAddr string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, RouteAdminInvites, strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set(adminTokenHeader, token)
	}
	req.RemoteAddr = remoteAddr + ":54321"
	return req
}

//  1. A wrong credential still passes through the limiter first, then is
//     rejected by the guard, and never reaches the handler.
func TestBootstrapRateLimit_FirstInvalidAttemptIsCountedThenRejected(t *testing.T) {
	recorder := newCountingRecorder()
	probe := &guardProbe{}
	rec := httptest.NewRecorder()

	bootstrapProbeRouter(t, recorder, probe).ServeHTTP(rec, bootstrapAttemptRequest("wrong-credential", "203.0.113.10"))

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for a wrong credential, got %d", rec.Code)
	}
	if recorder.calls != 1 {
		t.Fatalf("the attempt must be charged before the guard, got %d calls", recorder.calls)
	}
	if probe.guardCalls != 1 {
		t.Fatalf("expected the guard to run once, got %d", probe.guardCalls)
	}
	if probe.handlerCalls != 0 {
		t.Fatal("a wrong credential must never reach the handler")
	}
	// The key is the namespaced client IP, never the credential.
	if !strings.HasPrefix(recorder.lastKey, bootstrapLimiterNamespace+":") {
		t.Fatalf("unexpected limiter key namespace: %q", recorder.lastKey)
	}
	if strings.Contains(recorder.lastKey, "wrong-credential") {
		t.Fatalf("the limiter key must not contain the credential: %q", recorder.lastKey)
	}
}

//  2. Invalid attempts up to the limit all reach the guard and none reaches the
//     handler.
func TestBootstrapRateLimit_InvalidAttemptsUpToLimitReachGuardOnly(t *testing.T) {
	recorder := newCountingRecorder()
	probe := &guardProbe{}
	router := bootstrapProbeRouter(t, recorder, probe)

	for i := 0; i < 5; i++ {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, bootstrapAttemptRequest("wrong-credential", "203.0.113.11"))
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: expected 401, got %d", i+1, rec.Code)
		}
	}
	if probe.guardCalls != 5 {
		t.Fatalf("expected 5 guard calls within budget, got %d", probe.guardCalls)
	}
	if probe.handlerCalls != 0 {
		t.Fatal("no invalid attempt may reach the handler")
	}
}

//  3. Past the limit the request is refused without the guard ever running, so
//     the credential is not compared at all.
func TestBootstrapRateLimit_OverLimitReturns429WithoutReachingGuard(t *testing.T) {
	recorder := newCountingRecorder()
	probe := &guardProbe{}
	router := bootstrapProbeRouter(t, recorder, probe)

	for i := 0; i < 5; i++ {
		router.ServeHTTP(httptest.NewRecorder(), bootstrapAttemptRequest("wrong-credential", "203.0.113.12"))
	}
	guardCallsAtLimit := probe.guardCalls

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, bootstrapAttemptRequest("wrong-credential", "203.0.113.12"))

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429 past the budget, got %d", rec.Code)
	}
	if probe.guardCalls != guardCallsAtLimit {
		t.Fatal("a rate-limited request must not reach the credential comparison")
	}
	if probe.handlerCalls != 0 {
		t.Fatal("a rate-limited request must not reach the handler")
	}
	retryAfter := rec.Header().Get("Retry-After")
	if retryAfter == "" {
		t.Fatal("expected Retry-After on a 429")
	}
	if seconds, err := strconv.Atoi(retryAfter); err != nil || seconds <= 0 {
		t.Fatalf("expected a positive Retry-After, got %q", retryAfter)
	}
}

// 4. A valid credential inside the budget is accepted.
func TestBootstrapRateLimit_ValidCredentialWithinBudgetReachesHandler(t *testing.T) {
	recorder := newCountingRecorder()
	probe := &guardProbe{}
	rec := httptest.NewRecorder()

	bootstrapProbeRouter(t, recorder, probe).ServeHTTP(rec, bootstrapAttemptRequest(bootstrapToken, "203.0.113.13"))

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected the handler to run, got %d", rec.Code)
	}
	if probe.handlerCalls != 1 {
		t.Fatalf("expected one handler call, got %d", probe.handlerCalls)
	}
}

//  5. A valid credential past the budget is refused too. A correct credential
//     neither resets nor refunds the count: a stolen credential being replayed
//     is exactly the case the limit should still bound.
func TestBootstrapRateLimit_ValidCredentialOverLimitIsRefused(t *testing.T) {
	recorder := newCountingRecorder()
	probe := &guardProbe{}
	router := bootstrapProbeRouter(t, recorder, probe)

	for i := 0; i < 5; i++ {
		router.ServeHTTP(httptest.NewRecorder(), bootstrapAttemptRequest("wrong-credential", "203.0.113.14"))
	}

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, bootstrapAttemptRequest(bootstrapToken, "203.0.113.14"))

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429 even for a valid credential past the budget, got %d", rec.Code)
	}
	if probe.handlerCalls != 0 {
		t.Fatal("a rate-limited valid credential must not reach the handler")
	}
}

// 6. Budgets are per IP: exhausting one address must not lock out another.
func TestBootstrapRateLimit_BudgetsAreIndependentPerIP(t *testing.T) {
	recorder := newCountingRecorder()
	probe := &guardProbe{}
	router := bootstrapProbeRouter(t, recorder, probe)

	for i := 0; i < 6; i++ {
		router.ServeHTTP(httptest.NewRecorder(), bootstrapAttemptRequest("wrong-credential", "203.0.113.15"))
	}

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, bootstrapAttemptRequest(bootstrapToken, "198.51.100.7"))

	if rec.Code != http.StatusCreated {
		t.Fatalf("a different address must have its own budget, got %d", rec.Code)
	}
}

//  7. Two routers over one recorder are two replicas over one database: the
//     budget is shared, which is the property an in-process limiter lacks.
func TestBootstrapRateLimit_BudgetIsSharedAcrossRouterInstances(t *testing.T) {
	recorder := newCountingRecorder()
	replicaOne := bootstrapProbeRouter(t, recorder, &guardProbe{})
	replicaTwo := bootstrapProbeRouter(t, recorder, &guardProbe{})

	// Alternate between replicas so neither alone exceeds the budget.
	for i := 0; i < 5; i++ {
		router := replicaOne
		if i%2 == 1 {
			router = replicaTwo
		}
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, bootstrapAttemptRequest("wrong-credential", "203.0.113.16"))
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: expected 401 within the shared budget, got %d", i+1, rec.Code)
		}
	}

	rec := httptest.NewRecorder()
	replicaTwo.ServeHTTP(rec, bootstrapAttemptRequest("wrong-credential", "203.0.113.16"))

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("the sixth attempt across replicas must be refused, got %d", rec.Code)
	}
	if got := recorder.attempts(bootstrapLimiterNamespace + ":203.0.113.16"); got != 6 {
		t.Fatalf("expected one shared counter of 6, got %d", got)
	}
}

//  8. An unreachable counter fails closed: refusing is better than checking a
//     credential without a bound, and falling back to a per-process count would
//     silently reopen the multi-replica hole.
func TestBootstrapRateLimit_RecorderFailureFailsClosed(t *testing.T) {
	recorder := newCountingRecorder()
	recorder.err = errors.New("connection refused")
	probe := &guardProbe{}
	rec := httptest.NewRecorder()

	bootstrapProbeRouter(t, recorder, probe).ServeHTTP(rec, bootstrapAttemptRequest(bootstrapToken, "203.0.113.17"))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 when the counter is unreachable, got %d", rec.Code)
	}
	if probe.guardCalls != 0 {
		t.Fatal("an unavailable limiter must not let the credential be compared")
	}
	if probe.handlerCalls != 0 {
		t.Fatal("an unavailable limiter must not reach the handler")
	}
}

// An unwired or zero-budget configuration is the same refusal: zero never means
// unlimited on this route.
func TestBootstrapRateLimit_UnwiredOrZeroBudgetFailsClosed(t *testing.T) {
	for _, tt := range []struct {
		name string
		cfg  BootstrapRateLimitConfig
	}{
		{name: "nil recorder", cfg: BootstrapRateLimitConfig{Attempts: 5, Window: time.Minute}},
		{name: "zero attempts", cfg: BootstrapRateLimitConfig{Recorder: newCountingRecorder(), Window: time.Minute}},
		{name: "zero window", cfg: BootstrapRateLimitConfig{Recorder: newCountingRecorder(), Attempts: 5}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			reached := false
			handler := RateLimitBootstrapAttempts(tt.cfg)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				reached = true
			}))
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, bootstrapAttemptRequest(bootstrapToken, "203.0.113.18"))

			if rec.Code != http.StatusServiceUnavailable {
				t.Fatalf("expected 503, got %d", rec.Code)
			}
			if reached {
				t.Fatal("a misconfigured limiter must not call through")
			}
		})
	}
}

//  9. An oversized credential header is rejected generically, still spends
//     budget, and never reaches the guard or the handler.
func TestBootstrapRateLimit_OversizedHeaderIsRejectedAndCharged(t *testing.T) {
	recorder := newCountingRecorder()
	probe := &guardProbe{}
	rec := httptest.NewRecorder()

	oversized := strings.Repeat("A", maxAdminTokenHeaderBytes+1)
	bootstrapProbeRouter(t, recorder, probe).ServeHTTP(rec, bootstrapAttemptRequest(oversized, "203.0.113.19"))

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected a generic 401 for an oversized header, got %d", rec.Code)
	}
	if recorder.attempts(bootstrapLimiterNamespace+":203.0.113.19") != 1 {
		t.Fatal("an oversized header must still spend budget, or padding is a free probe")
	}
	if probe.guardCalls != 0 || probe.handlerCalls != 0 {
		t.Fatal("an oversized header must not reach the guard or the handler")
	}
	if strings.Contains(rec.Body.String(), "AAAA") {
		t.Fatalf("the rejection must not echo the header: %s", rec.Body.String())
	}
}

//  10. The limiter sits in front of the bootstrap lifecycle, not inside it: a
//     closed bootstrap window still answers 503 and is not reopened.
func TestBootstrapRateLimit_ClosedBootstrapWindowStillRefuses(t *testing.T) {
	invites := &inviteStub{err: domain.ErrBootstrapUnavailable}
	cfg := testConfig()
	cfg.AdminBootstrapToken = bootstrapToken
	router := NewRouter(cfg, platformlog.New("auth-service", "test"),
		&workspaceAdminStub{workspaceID: adminWorkspaceID}, nil, nil, nil, invites, nil,
		routerSessionStub{}, nil, nil, nil, newCountingRecorder())

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, bootstrapAttemptRequest(bootstrapToken, "203.0.113.20"))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected the closed bootstrap window to keep answering 503, got %d", rec.Code)
	}
}
