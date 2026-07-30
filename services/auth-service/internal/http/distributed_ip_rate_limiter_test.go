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

	"github.com/nicrepository/nchat/services/auth-service/internal/storage"
)

// The per-IP ceiling on invite creation used to be an in-process token bucket:
// N replicas meant N budgets, and a restart handed out a fresh one. These pin
// the replacement — one counter, shared, failing closed — while leaving the
// authoritative per-(actor, workspace) budget where it is.

const inviteIPLimit = 30

// sharedCounter is one budget per key, so two middlewares built over the same
// instance model two replicas over one database.
type sharedCounter struct {
	mu     sync.Mutex
	counts map[string]int
	err    error
	calls  int
	keys   []string
}

func newSharedCounter() *sharedCounter {
	return &sharedCounter{counts: make(map[string]int)}
}

func (c *sharedCounter) Allow(_ context.Context, req storage.DistributedRateLimitRequest) (storage.DistributedRateLimitResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	if c.err != nil {
		return storage.DistributedRateLimitResult{}, c.err
	}
	// Window is part of the key so a test can roll the window forward by
	// moving the injected clock, with no sleeping.
	key := req.Namespace + ":" + req.Subject + ":" + req.Now.UTC().Truncate(req.Window).String()
	c.keys = append(c.keys, key)
	c.counts[key]++
	if c.counts[key] <= req.Limit {
		return storage.DistributedRateLimitResult{Allowed: true}, nil
	}
	return storage.DistributedRateLimitResult{RetryAfter: req.Window}, nil
}

type ipLimiterProbe struct {
	mu           sync.Mutex
	handlerCalls int
}

func (p *ipLimiterProbe) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.handlerCalls
}

func ipLimitedHandler(counter DistributedRateLimiter, probe *ipLimiterProbe, now func() time.Time) http.Handler {
	return RateLimitByClientIP(DistributedIPRateLimitConfig{
		Limiter:   counter,
		Namespace: adminInviteIPLimiterNamespace,
		Limit:     inviteIPLimit,
		Window:    time.Hour,
		Now:       now,
	})(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		probe.mu.Lock()
		probe.handlerCalls++
		probe.mu.Unlock()
		w.WriteHeader(http.StatusCreated)
	}))
}

func ipRequest(remoteAddr string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, RouteAuthAdminInvites, strings.NewReader(`{}`))
	req.RemoteAddr = remoteAddr
	return req
}

func fixedClock(t time.Time) func() time.Time { return func() time.Time { return t } }

var limiterEpoch = time.Date(2026, 7, 30, 12, 30, 0, 0, time.UTC)

// 1–3. The whole budget is allowed; the next request is refused with
// Retry-After and never reaches the handler.
func TestInviteIPRateLimit_AllowsBudgetThenRefuses(t *testing.T) {
	counter := newSharedCounter()
	probe := &ipLimiterProbe{}
	handler := ipLimitedHandler(counter, probe, fixedClock(limiterEpoch))

	for i := 1; i <= inviteIPLimit; i++ {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, ipRequest("203.0.113.10:5001"))
		if rec.Code != http.StatusCreated {
			t.Fatalf("request %d of %d must be allowed, got %d", i, inviteIPLimit, rec.Code)
		}
	}

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, ipRequest("203.0.113.10:5001"))

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("request %d must be refused, got %d", inviteIPLimit+1, rec.Code)
	}
	if probe.count() != inviteIPLimit {
		t.Fatalf("the handler must run exactly %d times, got %d", inviteIPLimit, probe.count())
	}
	retryAfter := rec.Header().Get("Retry-After")
	if seconds, err := strconv.Atoi(retryAfter); err != nil || seconds <= 0 {
		t.Fatalf("expected a positive Retry-After, got %q", retryAfter)
	}
}

// 4 & 6. The budget is per address: a second address is unaffected, and two
// callers behind one address share it.
func TestInviteIPRateLimit_BudgetIsPerAddress(t *testing.T) {
	counter := newSharedCounter()
	probe := &ipLimiterProbe{}
	handler := ipLimitedHandler(counter, probe, fixedClock(limiterEpoch))

	for i := 0; i < inviteIPLimit+1; i++ {
		handler.ServeHTTP(httptest.NewRecorder(), ipRequest("203.0.113.11:5001"))
	}

	// Different address, its own budget.
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, ipRequest("198.51.100.7:5001"))
	if rec.Code != http.StatusCreated {
		t.Fatalf("a different address must have its own budget, got %d", rec.Code)
	}

	// Same address, different source port — still one caller, one budget.
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, ipRequest("203.0.113.11:9999"))
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("the port must not create a second budget, got %d", rec.Code)
	}
}

// 5. The IP ceiling is keyed on the address alone, so one identity moving
// between addresses gets a fresh IP allowance — which is exactly why it is the
// complementary control and the per-(actor, workspace) budget is the
// authoritative one.
func TestInviteIPRateLimit_IsIndependentOfCallerIdentity(t *testing.T) {
	counter := newSharedCounter()
	probe := &ipLimiterProbe{}
	handler := ipLimitedHandler(counter, probe, fixedClock(limiterEpoch))

	for i := 0; i < inviteIPLimit+1; i++ {
		handler.ServeHTTP(httptest.NewRecorder(), ipRequest("203.0.113.12:5001"))
	}

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, ipRequest("203.0.113.13:5001"))
	if rec.Code != http.StatusCreated {
		t.Fatalf("the same identity from another address has its own IP budget, got %d", rec.Code)
	}
}

// 7. An unreachable counter refuses rather than admitting the request: falling
// through would make the ceiling advisory.
func TestInviteIPRateLimit_BackendFailureFailsClosed(t *testing.T) {
	counter := newSharedCounter()
	counter.err = errors.New("connection refused")
	probe := &ipLimiterProbe{}
	rec := httptest.NewRecorder()

	ipLimitedHandler(counter, probe, fixedClock(limiterEpoch)).ServeHTTP(rec, ipRequest("203.0.113.14:5001"))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 when the counter is unreachable, got %d", rec.Code)
	}
	if probe.count() != 0 {
		t.Fatal("an unavailable counter must not let the request through")
	}
}

// An unwired or zero budget is the same refusal: zero never means unlimited.
func TestInviteIPRateLimit_UnwiredOrZeroBudgetFailsClosed(t *testing.T) {
	for _, tt := range []struct {
		name string
		cfg  DistributedIPRateLimitConfig
	}{
		{name: "nil limiter", cfg: DistributedIPRateLimitConfig{Limit: 30, Window: time.Hour}},
		{name: "zero limit", cfg: DistributedIPRateLimitConfig{Limiter: newSharedCounter(), Window: time.Hour}},
		{name: "zero window", cfg: DistributedIPRateLimitConfig{Limiter: newSharedCounter(), Limit: 30}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			reached := false
			handler := RateLimitByClientIP(tt.cfg)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				reached = true
			}))
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, ipRequest("203.0.113.15:5001"))

			if rec.Code != http.StatusServiceUnavailable {
				t.Fatalf("expected 503, got %d", rec.Code)
			}
			if reached {
				t.Fatal("a misconfigured limiter must not call through")
			}
		})
	}
}

// 8 & 9. The key comes from the shared client-IP helper, which canonicalises
// the address — an IPv4-mapped IPv6 peer and its IPv4 form are one caller with
// one budget, not two.
func TestInviteIPRateLimit_NormalizesAddressForms(t *testing.T) {
	counter := newSharedCounter()
	probe := &ipLimiterProbe{}
	handler := ipLimitedHandler(counter, probe, fixedClock(limiterEpoch))

	handler.ServeHTTP(httptest.NewRecorder(), ipRequest("203.0.113.16:5001"))
	handler.ServeHTTP(httptest.NewRecorder(), ipRequest("[::ffff:203.0.113.16]:5002"))

	counter.mu.Lock()
	defer counter.mu.Unlock()
	if len(counter.keys) != 2 {
		t.Fatalf("expected two charges, got %d", len(counter.keys))
	}
	if counter.keys[0] != counter.keys[1] {
		t.Fatalf("the two address forms must collapse to one budget:\n%s\n%s", counter.keys[0], counter.keys[1])
	}
}

// A malformed RemoteAddr still produces a key and still spends budget: it is
// never a way past the ceiling.
func TestInviteIPRateLimit_MalformedAddressIsStillCharged(t *testing.T) {
	counter := newSharedCounter()
	probe := &ipLimiterProbe{}
	handler := ipLimitedHandler(counter, probe, fixedClock(limiterEpoch))

	for i := 0; i < inviteIPLimit+1; i++ {
		req := httptest.NewRequest(http.MethodPost, RouteAuthAdminInvites, strings.NewReader(`{}`))
		req.RemoteAddr = "not-an-address"
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if i == inviteIPLimit && rec.Code != http.StatusTooManyRequests {
			t.Fatalf("a malformed address must not bypass the ceiling, got %d", rec.Code)
		}
	}
}

// 10. The budget renews with the window, exercised by moving the injected
// clock rather than waiting an hour.
func TestInviteIPRateLimit_BudgetRenewsInTheNextWindow(t *testing.T) {
	counter := newSharedCounter()
	probe := &ipLimiterProbe{}
	clock := limiterEpoch
	handler := ipLimitedHandler(counter, probe, func() time.Time { return clock })

	for i := 0; i < inviteIPLimit; i++ {
		handler.ServeHTTP(httptest.NewRecorder(), ipRequest("203.0.113.17:5001"))
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, ipRequest("203.0.113.17:5001"))
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("the budget must be spent before the window advances, got %d", rec.Code)
	}

	clock = clock.Add(time.Hour)

	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, ipRequest("203.0.113.17:5001"))
	if rec.Code != http.StatusCreated {
		t.Fatalf("the budget must renew in the next window, got %d", rec.Code)
	}
}

// Two middlewares over one counter are two replicas over one database:
// alternating between them spends a single budget, which the in-process bucket
// this replaced could not do.
func TestInviteIPRateLimit_BudgetIsSharedAcrossReplicas(t *testing.T) {
	counter := newSharedCounter()
	replicaOne := ipLimitedHandler(counter, &ipLimiterProbe{}, fixedClock(limiterEpoch))
	replicaTwo := ipLimitedHandler(counter, &ipLimiterProbe{}, fixedClock(limiterEpoch))

	for i := 0; i < inviteIPLimit; i++ {
		handler := replicaOne
		if i%2 == 1 {
			handler = replicaTwo
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, ipRequest("203.0.113.18:5001"))
		if rec.Code != http.StatusCreated {
			t.Fatalf("request %d must be allowed within the shared budget, got %d", i+1, rec.Code)
		}
	}

	// A third instance stands in for a restarted pod: the count is in the
	// counter, not in the process, so it does not start over.
	restarted := ipLimitedHandler(counter, &ipLimiterProbe{}, fixedClock(limiterEpoch))
	rec := httptest.NewRecorder()
	restarted.ServeHTTP(rec, ipRequest("203.0.113.18:5001"))
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("a restart must not renew the budget, got %d", rec.Code)
	}
}

// The invite ceiling and the bootstrap budget share a table but not an
// allowance: spending one must not consume the other.
func TestInviteIPRateLimit_NamespaceIsolatesFromBootstrapBudget(t *testing.T) {
	counter := newSharedCounter()
	probe := &ipLimiterProbe{}
	handler := ipLimitedHandler(counter, probe, fixedClock(limiterEpoch))

	handler.ServeHTTP(httptest.NewRecorder(), ipRequest("203.0.113.19:5001"))

	counter.mu.Lock()
	defer counter.mu.Unlock()
	if len(counter.keys) != 1 || !strings.HasPrefix(counter.keys[0], adminInviteIPLimiterNamespace+":") {
		t.Fatalf("the invite ceiling must carry its own namespace, got %v", counter.keys)
	}
	if strings.Contains(counter.keys[0], bootstrapLimiterNamespace) {
		t.Fatalf("the invite ceiling must not share the bootstrap namespace: %s", counter.keys[0])
	}
}
