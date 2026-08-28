package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
)

// ── Test doubles ─────────────────────────────────────────────────────────────

// stubWorkspaceResolver stands in for the canonical, server-side answer to
// "which workspace does this request belong to".
type stubWorkspaceResolver struct {
	mu  sync.Mutex
	id  string
	err error
}

func (s *stubWorkspaceResolver) ResolveWorkspaceID(context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.id, s.err
}

func (s *stubWorkspaceResolver) set(id string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.id, s.err = id, err
}

// stubPolicySource serves per-workspace policies and counts reads per
// workspace, so tests can assert both isolation and caching without a real
// clock or a real database.
type stubPolicySource struct {
	mu       sync.Mutex
	policies map[string]int
	errs     map[string]error
	reads    map[string]int
}

func newStubPolicySource() *stubPolicySource {
	return &stubPolicySource{
		policies: make(map[string]int),
		errs:     make(map[string]error),
		reads:    make(map[string]int),
	}
}

func (s *stubPolicySource) GetWorkspaceByID(_ context.Context, id string) (domain.Workspace, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reads[id]++
	if err := s.errs[id]; err != nil {
		return domain.Workspace{}, err
	}
	perMinute, ok := s.policies[id]
	if !ok {
		return domain.Workspace{}, domain.ErrNotFound
	}
	return domain.Workspace{ID: id, MessageRateLimitPerMinute: perMinute}, nil
}

func (s *stubPolicySource) set(id string, perMinute int) *stubPolicySource {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.policies[id] = perMinute
	delete(s.errs, id)
	return s
}

func (s *stubPolicySource) fail(id string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.errs[id] = err
}

func (s *stubPolicySource) readCount(id string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reads[id]
}

// countingLimiter stands in for the shared Lua/Valkey limiter. It reproduces
// the fixed-window semantics that matter to these tests: one counter per
// (action, user) key, rejected attempts still increment it, and the caller's
// budget is whatever it passes in.
type countingLimiter struct {
	mu      sync.Mutex
	counts  map[string]int
	budgets map[string]int
	err     error
	lastMax int
	lastWin int
}

func newCountingLimiter() *countingLimiter {
	return &countingLimiter{counts: make(map[string]int), budgets: make(map[string]int)}
}

func (l *countingLimiter) AllowActionWithLimit(_ context.Context, userID, action string, maxActions, windowSeconds int) (bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.err != nil {
		return false, l.err
	}
	l.lastMax, l.lastWin = maxActions, windowSeconds
	key := action + "|" + userID
	l.counts[key]++
	l.budgets[key] = maxActions
	return l.counts[key] <= maxActions, nil
}

func (l *countingLimiter) count(action, userID string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.counts[action+"|"+userID]
}

// budgetFor reports the budget the guard supplied for a workspace/user pair,
// which is how these tests observe "which policy was applied" without reaching
// into the guard's cache.
func (l *countingLimiter) budgetFor(workspaceID, userID string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.budgets["send_message:"+workspaceID+"|"+userID]
}

// newTestGuard builds a guard with a controllable clock.
func newTestGuard(t *testing.T, resolver *stubWorkspaceResolver, policies *stubPolicySource, limiter antiSpamLimiter) (*AntiSpamGuard, func(time.Duration)) {
	t.Helper()
	var mu sync.Mutex
	current := time.Unix(1_700_000_000, 0)
	guard := NewAntiSpamGuard(resolver, policies, limiter)
	if guard == nil {
		t.Fatal("NewAntiSpamGuard returned nil for non-nil dependencies")
	}
	guard.now = func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		return current
	}
	return guard, func(d time.Duration) {
		mu.Lock()
		defer mu.Unlock()
		current = current.Add(d)
	}
}

// countingHandler records whether the guarded handler ran — the proxy for "the
// message was persisted and published", which happens downstream of it.
type countingHandler struct {
	mu           sync.Mutex
	calls        int
	workspaceIDs []string
}

func (h *countingHandler) ServeHTTP(_ http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.calls++
	// The guard publishes the canonical workspace for downstream handlers; this
	// records what they would read.
	h.workspaceIDs = append(h.workspaceIDs, contextWorkspaceID(r.Context()))
}

func (h *countingHandler) callCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.calls
}

func guardedRequest(t *testing.T, guard *AntiSpamGuard, next http.Handler, userID string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/api/chat/channels/x/messages", nil)
	if userID != "" {
		request = request.WithContext(context.WithValue(request.Context(), ctxKeyUserID, userID))
	}
	recorder := httptest.NewRecorder()
	guard.Middleware(next).ServeHTTP(recorder, request)
	return recorder
}

// ── Enforcement ──────────────────────────────────────────────────────────────

func TestAntiSpamGuard_AllowsUpToTheConfiguredLimitThenRejects(t *testing.T) {
	guard, _ := newTestGuard(t,
		&stubWorkspaceResolver{id: "ws-1"},
		newStubPolicySource().set("ws-1", 3),
		newCountingLimiter(),
	)
	next := &countingHandler{}

	for i := 1; i <= 3; i++ {
		if code := guardedRequest(t, guard, next, "user-1").Code; code == http.StatusTooManyRequests {
			t.Fatalf("message %d of 3 rejected", i)
		}
	}
	if next.callCount() != 3 {
		t.Fatalf("expected 3 messages through, got %d", next.callCount())
	}

	recorder := guardedRequest(t, guard, next, "user-1")
	if recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429 past the limit, got %d", recorder.Code)
	}
	// A rejected message must never reach the handler: it is the handler that
	// persists it and broadcasts it over the hub.
	if next.callCount() != 3 {
		t.Fatalf("rejected message reached the handler: %d calls", next.callCount())
	}
	if got := recorder.Header().Get("Retry-After"); got != "60" {
		t.Fatalf("expected Retry-After 60, got %q", got)
	}
}

func TestAntiSpamGuard_UsesTheWindowAndBudgetFromThePolicy(t *testing.T) {
	limiter := newCountingLimiter()
	guard, _ := newTestGuard(t,
		&stubWorkspaceResolver{id: "ws-1"},
		newStubPolicySource().set("ws-1", 42),
		limiter,
	)

	guardedRequest(t, guard, &countingHandler{}, "user-1")

	if limiter.lastMax != 42 {
		t.Fatalf("expected the workspace policy as the budget, got %d", limiter.lastMax)
	}
	if limiter.lastWin != 60 {
		t.Fatalf("RF-19 is a per-minute limit, got a %ds window", limiter.lastWin)
	}
}

func TestAntiSpamGuard_PublishesTheCanonicalWorkspaceForDownstreamHandlers(t *testing.T) {
	guard, _ := newTestGuard(t,
		&stubWorkspaceResolver{id: "ws-canonical"},
		newStubPolicySource().set("ws-canonical", 10),
		newCountingLimiter(),
	)
	next := &countingHandler{}

	guardedRequest(t, guard, next, "user-1")

	// The handler must write the message to the same workspace the send was
	// counted against; it reads that from the context rather than resolving
	// again.
	if len(next.workspaceIDs) != 1 || next.workspaceIDs[0] != "ws-canonical" {
		t.Fatalf("expected the canonical workspace in context, got %v", next.workspaceIDs)
	}
}

func TestAntiSpamGuard_UsersHaveIndependentCounters(t *testing.T) {
	guard, _ := newTestGuard(t,
		&stubWorkspaceResolver{id: "ws-1"},
		newStubPolicySource().set("ws-1", 1),
		newCountingLimiter(),
	)
	next := &countingHandler{}

	guardedRequest(t, guard, next, "user-1")
	if code := guardedRequest(t, guard, next, "user-1").Code; code != http.StatusTooManyRequests {
		t.Fatalf("expected user-1 to be blocked, got %d", code)
	}
	if code := guardedRequest(t, guard, next, "user-2").Code; code == http.StatusTooManyRequests {
		t.Fatal("user-2 must not be affected by user-1 exhausting their budget")
	}
}

// Two guards sharing one limiter stand in for two chat-service replicas sharing
// one Valkey: the budget is spent once, not once per instance. This is what the
// previous in-memory limiter could not do.
func TestAntiSpamGuard_InstancesShareOneBudget(t *testing.T) {
	limiter := newCountingLimiter()
	instanceA, _ := newTestGuard(t, &stubWorkspaceResolver{id: "ws-1"}, newStubPolicySource().set("ws-1", 2), limiter)
	instanceB, _ := newTestGuard(t, &stubWorkspaceResolver{id: "ws-1"}, newStubPolicySource().set("ws-1", 2), limiter)

	if code := guardedRequest(t, instanceA, &countingHandler{}, "user-1").Code; code == http.StatusTooManyRequests {
		t.Fatal("first message rejected")
	}
	if code := guardedRequest(t, instanceB, &countingHandler{}, "user-1").Code; code == http.StatusTooManyRequests {
		t.Fatal("second message rejected")
	}
	if code := guardedRequest(t, instanceA, &countingHandler{}, "user-1").Code; code != http.StatusTooManyRequests {
		t.Fatalf("third message must exceed the shared budget of 2, got %d", code)
	}
}

func TestAntiSpamGuard_UnauthenticatedRequestsPassThrough(t *testing.T) {
	guard, _ := newTestGuard(t,
		&stubWorkspaceResolver{id: "ws-1"},
		newStubPolicySource().set("ws-1", 1),
		newCountingLimiter(),
	)
	next := &countingHandler{}

	// No user in context: the auth middlewares upstream own this rejection, and
	// counting an unauthenticated identity is not meaningful.
	for range 5 {
		guardedRequest(t, guard, next, "")
	}
	if next.callCount() != 5 {
		t.Fatalf("expected pass-through, got %d calls", next.callCount())
	}
}

// ── Cross-workspace isolation (Security Review finding) ──────────────────────

// Each workspace is judged by its own policy. Before the fix the guard resolved
// one workspace for every request and cached a single policy, so whichever
// workspace was read first decided the limit for all of them.
func TestAntiSpamGuard_EachWorkspaceUsesItsOwnPolicy(t *testing.T) {
	limiter := newCountingLimiter()
	policies := newStubPolicySource().set("ws-a", 1).set("ws-b", 3)
	resolver := &stubWorkspaceResolver{id: "ws-a"}
	guard, _ := newTestGuard(t, resolver, policies, limiter)
	next := &countingHandler{}

	// Workspace A: budget of 1.
	if code := guardedRequest(t, guard, next, "user-1").Code; code == http.StatusTooManyRequests {
		t.Fatal("first message in ws-a rejected")
	}
	if code := guardedRequest(t, guard, next, "user-1").Code; code != http.StatusTooManyRequests {
		t.Fatalf("ws-a must block past its budget of 1, got %d", code)
	}

	// Workspace B: budget of 3, unaffected by A being exhausted.
	resolver.set("ws-b", nil)
	for i := 1; i <= 3; i++ {
		if code := guardedRequest(t, guard, next, "user-1").Code; code == http.StatusTooManyRequests {
			t.Fatalf("message %d of 3 in ws-b rejected — ws-b is using ws-a's policy", i)
		}
	}
	if code := guardedRequest(t, guard, next, "user-1").Code; code != http.StatusTooManyRequests {
		t.Fatalf("ws-b must block past its own budget of 3, got %d", code)
	}

	if got := limiter.budgetFor("ws-a", "user-1"); got != 1 {
		t.Fatalf("ws-a was enforced with budget %d, want 1", got)
	}
	if got := limiter.budgetFor("ws-b", "user-1"); got != 3 {
		t.Fatalf("ws-b was enforced with budget %d, want 3", got)
	}
}

// The same user sending in two workspaces spends two budgets, not one.
func TestAntiSpamGuard_SameUserHasIndependentBudgetsPerWorkspace(t *testing.T) {
	limiter := newCountingLimiter()
	resolver := &stubWorkspaceResolver{id: "ws-a"}
	guard, _ := newTestGuard(t, resolver, newStubPolicySource().set("ws-a", 1).set("ws-b", 1), limiter)
	next := &countingHandler{}

	guardedRequest(t, guard, next, "user-1")
	if code := guardedRequest(t, guard, next, "user-1").Code; code != http.StatusTooManyRequests {
		t.Fatalf("ws-a budget of 1 must be spent, got %d", code)
	}

	resolver.set("ws-b", nil)
	if code := guardedRequest(t, guard, next, "user-1").Code; code == http.StatusTooManyRequests {
		t.Fatal("sending in ws-a consumed the ws-b budget")
	}

	if got := limiter.count("send_message:ws-a", "user-1"); got != 2 {
		t.Fatalf("expected 2 counts in ws-a, got %d", got)
	}
	if got := limiter.count("send_message:ws-b", "user-1"); got != 1 {
		t.Fatalf("expected 1 count in ws-b, got %d", got)
	}
}

// The direct reproduction of the finding: a workspace that is not the default
// must be judged by its own policy, never by the default workspace's.
func TestAntiSpamGuard_NonDefaultWorkspaceDoesNotInheritTheDefaultPolicy(t *testing.T) {
	limiter := newCountingLimiter()
	policies := newStubPolicySource().
		set("ws-default", 1). // the default workspace, deliberately restrictive
		set("ws-other", 5)    // a different workspace, deliberately permissive
	resolver := &stubWorkspaceResolver{id: "ws-default"}
	guard, _ := newTestGuard(t, resolver, policies, limiter)
	next := &countingHandler{}

	// Warm the guard on the default workspace and exhaust it.
	guardedRequest(t, guard, next, "user-1")
	if code := guardedRequest(t, guard, next, "user-1").Code; code != http.StatusTooManyRequests {
		t.Fatalf("default workspace budget of 1 must be spent, got %d", code)
	}

	// Now send in a different workspace. Its own policy of 5 must apply, and it
	// must not be blocked by the default workspace's exhausted counter.
	resolver.set("ws-other", nil)
	for i := 1; i <= 5; i++ {
		if code := guardedRequest(t, guard, next, "user-1").Code; code == http.StatusTooManyRequests {
			t.Fatalf("message %d in ws-other rejected — the default workspace's policy or budget leaked", i)
		}
	}
	if got := limiter.budgetFor("ws-other", "user-1"); got != 5 {
		t.Fatalf("ws-other enforced with budget %d, want its own 5", got)
	}
	if policies.readCount("ws-other") == 0 {
		t.Fatal("ws-other's policy was never read; the guard answered from another workspace's cache")
	}
}

// ── Policy loading and cache ─────────────────────────────────────────────────

func TestAntiSpamGuard_CachesThePolicyWithinTheTTL(t *testing.T) {
	policies := newStubPolicySource().set("ws-1", 100)
	guard, _ := newTestGuard(t, &stubWorkspaceResolver{id: "ws-1"}, policies, newCountingLimiter())

	for range 10 {
		guardedRequest(t, guard, &countingHandler{}, "user-1")
	}
	if policies.readCount("ws-1") != 1 {
		t.Fatalf("expected 1 database read for 10 messages, got %d", policies.readCount("ws-1"))
	}
}

func TestAntiSpamGuard_CacheEntriesAreIndependentPerWorkspace(t *testing.T) {
	limiter := newCountingLimiter()
	policies := newStubPolicySource().set("ws-a", 10).set("ws-b", 20)
	resolver := &stubWorkspaceResolver{id: "ws-a"}
	guard, advance := newTestGuard(t, resolver, policies, limiter)

	guardedRequest(t, guard, &countingHandler{}, "user-1")
	resolver.set("ws-b", nil)
	guardedRequest(t, guard, &countingHandler{}, "user-1")

	// Two independent entries: reading B did not evict or overwrite A.
	resolver.set("ws-a", nil)
	guardedRequest(t, guard, &countingHandler{}, "user-1")
	if policies.readCount("ws-a") != 1 {
		t.Fatalf("ws-a was re-read %d times; its cache entry was clobbered by ws-b", policies.readCount("ws-a"))
	}
	if got := limiter.budgetFor("ws-a", "user-1"); got != 10 {
		t.Fatalf("ws-a enforced with budget %d, want 10", got)
	}

	// Expiry is per entry: advancing past the TTL re-reads both, independently.
	advance(antiSpamPolicyTTL + time.Second)
	policies.set("ws-a", 11)
	guardedRequest(t, guard, &countingHandler{}, "user-1")
	if got := limiter.budgetFor("ws-a", "user-1"); got != 11 {
		t.Fatalf("expected ws-a to pick up its own new policy, got %d", got)
	}
	if got := limiter.budgetFor("ws-b", "user-1"); got != 20 {
		t.Fatalf("ws-b's cached policy changed to %d when ws-a expired", got)
	}
}

func TestAntiSpamGuard_LowerLimitTakesEffectAfterTheTTL(t *testing.T) {
	limiter := newCountingLimiter()
	policies := newStubPolicySource().set("ws-1", 100)
	guard, advance := newTestGuard(t, &stubWorkspaceResolver{id: "ws-1"}, policies, limiter)

	guardedRequest(t, guard, &countingHandler{}, "user-1")
	if limiter.lastMax != 100 {
		t.Fatalf("expected the initial budget, got %d", limiter.lastMax)
	}

	policies.set("ws-1", 5)
	advance(antiSpamPolicyTTL + time.Second)

	guardedRequest(t, guard, &countingHandler{}, "user-1")
	if limiter.lastMax != 5 {
		t.Fatalf("expected the lowered budget to apply, got %d", limiter.lastMax)
	}
}

func TestAntiSpamGuard_HigherLimitTakesEffectAfterTheTTL(t *testing.T) {
	limiter := newCountingLimiter()
	policies := newStubPolicySource().set("ws-1", 5)
	guard, advance := newTestGuard(t, &stubWorkspaceResolver{id: "ws-1"}, policies, limiter)

	guardedRequest(t, guard, &countingHandler{}, "user-1")

	policies.set("ws-1", 200)
	advance(antiSpamPolicyTTL + time.Second)

	guardedRequest(t, guard, &countingHandler{}, "user-1")
	if limiter.lastMax != 200 {
		t.Fatalf("expected the raised budget to apply, got %d", limiter.lastMax)
	}
}

func TestAntiSpamGuard_UnsetPolicyFallsBackToTheDefaultLimit(t *testing.T) {
	// A workspace row written before migration 000018 scans as zero.
	limiter := newCountingLimiter()
	guard, _ := newTestGuard(t, &stubWorkspaceResolver{id: "ws-1"}, newStubPolicySource().set("ws-1", 0), limiter)

	guardedRequest(t, guard, &countingHandler{}, "user-1")

	if limiter.lastMax != domain.DefaultMessageRateLimitPerMinute {
		t.Fatalf("expected the default budget, got %d", limiter.lastMax)
	}
}

// ── Invalidation ─────────────────────────────────────────────────────────────

func TestAntiSpamGuard_InvalidateAppliesTheChangeWithoutWaitingTheTTL(t *testing.T) {
	limiter := newCountingLimiter()
	policies := newStubPolicySource().set("ws-1", 100)
	guard, _ := newTestGuard(t, &stubWorkspaceResolver{id: "ws-1"}, policies, limiter)

	guardedRequest(t, guard, &countingHandler{}, "user-1")
	policies.set("ws-1", 5)

	// Without invalidation the guard would keep the cached 100 until the TTL.
	guard.Invalidate("ws-1")
	guardedRequest(t, guard, &countingHandler{}, "user-1")
	if limiter.lastMax != 5 {
		t.Fatalf("expected the new budget immediately after invalidation, got %d", limiter.lastMax)
	}
}

// An admin saving in one workspace must not force another workspace back to a
// database read, and must never replace its policy.
func TestAntiSpamGuard_InvalidateTouchesOnlyTheNamedWorkspace(t *testing.T) {
	limiter := newCountingLimiter()
	policies := newStubPolicySource().set("ws-a", 10).set("ws-b", 20)
	resolver := &stubWorkspaceResolver{id: "ws-a"}
	guard, _ := newTestGuard(t, resolver, policies, limiter)

	// Warm both.
	guardedRequest(t, guard, &countingHandler{}, "user-1")
	resolver.set("ws-b", nil)
	guardedRequest(t, guard, &countingHandler{}, "user-1")

	// Update and invalidate B only.
	policies.set("ws-b", 25)
	guard.Invalidate("ws-b")
	guardedRequest(t, guard, &countingHandler{}, "user-1")
	if got := limiter.budgetFor("ws-b", "user-1"); got != 25 {
		t.Fatalf("ws-b did not pick up its new policy, got %d", got)
	}

	// A is untouched: still cached, still its own value.
	resolver.set("ws-a", nil)
	guardedRequest(t, guard, &countingHandler{}, "user-1")
	if policies.readCount("ws-a") != 1 {
		t.Fatalf("invalidating ws-b evicted ws-a: %d reads", policies.readCount("ws-a"))
	}
	if got := limiter.budgetFor("ws-a", "user-1"); got != 10 {
		t.Fatalf("ws-a's policy became %d after ws-b was invalidated", got)
	}
}

func TestAntiSpamGuard_InvalidateIgnoresEmptyAndNilReceiver(t *testing.T) {
	policies := newStubPolicySource().set("ws-1", 10)
	guard, _ := newTestGuard(t, &stubWorkspaceResolver{id: "ws-1"}, policies, newCountingLimiter())

	guardedRequest(t, guard, &countingHandler{}, "user-1")
	guard.Invalidate("")
	guardedRequest(t, guard, &countingHandler{}, "user-1")
	if policies.readCount("ws-1") != 1 {
		t.Fatalf("an empty workspace ID cleared the cache: %d reads", policies.readCount("ws-1"))
	}

	var nilGuard *AntiSpamGuard
	nilGuard.Invalidate("ws-1") // must not panic
}

// ── Failure behaviour ────────────────────────────────────────────────────────

func TestAntiSpamGuard_UnresolvableWorkspaceRefusesTheSend(t *testing.T) {
	limiter := newCountingLimiter()
	guard, _ := newTestGuard(t,
		&stubWorkspaceResolver{err: errors.New("connection refused")},
		newStubPolicySource().set("ws-default", 60),
		limiter,
	)
	next := &countingHandler{}

	recorder := guardedRequest(t, guard, next, "user-1")

	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", recorder.Code)
	}
	if next.callCount() != 0 {
		t.Fatal("an unattributable message was admitted")
	}
	// Critically: no counter was touched at all, so the send could not have been
	// charged to some other workspace's budget.
	if got := limiter.count("send_message:ws-default", "user-1"); got != 0 {
		t.Fatalf("the send consumed the default workspace's budget: %d", got)
	}
}

func TestAntiSpamGuard_EmptyWorkspaceRefusesTheSend(t *testing.T) {
	guard, _ := newTestGuard(t, &stubWorkspaceResolver{id: ""}, newStubPolicySource(), newCountingLimiter())
	next := &countingHandler{}

	if code := guardedRequest(t, guard, next, "user-1").Code; code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 for an empty workspace, got %d", code)
	}
	if next.callCount() != 0 {
		t.Fatal("a message with no workspace was admitted")
	}
}

func TestAntiSpamGuard_AllowRejectsMissingIdentifiers(t *testing.T) {
	guard, _ := newTestGuard(t, &stubWorkspaceResolver{id: "ws-1"}, newStubPolicySource().set("ws-1", 10), newCountingLimiter())

	if allowed, err := guard.Allow(context.Background(), "user-1", ""); allowed || err == nil {
		t.Fatal("an empty workspace must be an error, never a default")
	}
	if allowed, err := guard.Allow(context.Background(), "", "ws-1"); allowed || err == nil {
		t.Fatal("an empty user must be an error")
	}
}

func TestAntiSpamGuard_DatabaseFailureKeepsTheLastKnownPolicy(t *testing.T) {
	limiter := newCountingLimiter()
	policies := newStubPolicySource().set("ws-1", 7)
	guard, advance := newTestGuard(t, &stubWorkspaceResolver{id: "ws-1"}, policies, limiter)

	guardedRequest(t, guard, &countingHandler{}, "user-1")

	policies.fail("ws-1", errors.New("connection refused"))
	advance(antiSpamPolicyTTL + time.Second)

	next := &countingHandler{}
	if code := guardedRequest(t, guard, next, "user-1").Code; code == http.StatusTooManyRequests {
		t.Fatal("a readable stale policy must keep serving, not block everything")
	}
	if limiter.lastMax != 7 {
		t.Fatalf("expected the stale budget of 7 to remain enforced, got %d", limiter.lastMax)
	}
	if next.callCount() != 1 {
		t.Fatal("the message should have been served under the stale policy")
	}
}

// Stale is per workspace: a workspace whose policy has never been read cannot
// be served from another workspace's stale entry.
func TestAntiSpamGuard_StaleCacheIsNotSharedAcrossWorkspaces(t *testing.T) {
	limiter := newCountingLimiter()
	policies := newStubPolicySource().set("ws-a", 7)
	resolver := &stubWorkspaceResolver{id: "ws-a"}
	guard, advance := newTestGuard(t, resolver, policies, limiter)

	// Warm A, then break every read.
	guardedRequest(t, guard, &countingHandler{}, "user-1")
	policies.fail("ws-a", errors.New("connection refused"))
	policies.fail("ws-b", errors.New("connection refused"))
	advance(antiSpamPolicyTTL + time.Second)

	// B has no entry of its own and must be refused, not served with A's.
	resolver.set("ws-b", nil)
	next := &countingHandler{}
	recorder := guardedRequest(t, guard, next, "user-1")

	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 for ws-b, got %d", recorder.Code)
	}
	if next.callCount() != 0 {
		t.Fatal("ws-b was admitted using ws-a's stale policy")
	}
	if got := limiter.count("send_message:ws-b", "user-1"); got != 0 {
		t.Fatalf("ws-b consumed a budget it has no policy for: %d", got)
	}
	// A still serves from its own stale entry.
	resolver.set("ws-a", nil)
	if code := guardedRequest(t, guard, &countingHandler{}, "user-1").Code; code == http.StatusServiceUnavailable {
		t.Fatal("ws-a lost its own stale policy")
	}
}

func TestAntiSpamGuard_DatabaseFailureWithNoCachedPolicyRefusesTheSend(t *testing.T) {
	policies := newStubPolicySource()
	policies.fail("ws-1", errors.New("connection refused"))
	guard, _ := newTestGuard(t, &stubWorkspaceResolver{id: "ws-1"}, policies, newCountingLimiter())
	next := &countingHandler{}

	recorder := guardedRequest(t, guard, next, "user-1")

	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", recorder.Code)
	}
	// The critical property: a policy that cannot be read must not become an
	// unmetered send path.
	if next.callCount() != 0 {
		t.Fatal("an uncounted message was admitted while the policy was unreadable")
	}
}

func TestAntiSpamGuard_LimiterFailureRefusesTheSend(t *testing.T) {
	limiter := newCountingLimiter()
	limiter.err = errors.New("valkey unreachable")
	guard, _ := newTestGuard(t, &stubWorkspaceResolver{id: "ws-1"}, newStubPolicySource().set("ws-1", 60), limiter)
	next := &countingHandler{}

	recorder := guardedRequest(t, guard, next, "user-1")

	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 when the counter is unavailable, got %d", recorder.Code)
	}
	if next.callCount() != 0 {
		t.Fatal("a message was persisted while the limiter could not count it")
	}
}

func TestAntiSpamGuard_FailureBodiesCarryNoInternalDetail(t *testing.T) {
	limiter := newCountingLimiter()
	limiter.err = errors.New("dial tcp 10.0.0.5:6379: connect: connection refused")
	guard, _ := newTestGuard(t, &stubWorkspaceResolver{id: "ws-secret"}, newStubPolicySource().set("ws-secret", 60), limiter)

	body := guardedRequest(t, guard, &countingHandler{}, "user-1").Body.String()

	for _, leak := range []string{"6379", "10.0.0.5", "ws-secret", "send_message"} {
		if strings.Contains(body, leak) {
			t.Fatalf("error body leaked %q: %s", leak, body)
		}
	}
}

func TestNewAntiSpamGuard_RequiresEveryDependency(t *testing.T) {
	resolver := &stubWorkspaceResolver{id: "ws-1"}
	policies := newStubPolicySource()
	limiter := newCountingLimiter()

	if NewAntiSpamGuard(nil, policies, limiter) != nil {
		t.Fatal("expected nil without a workspace resolver")
	}
	if NewAntiSpamGuard(resolver, nil, limiter) != nil {
		t.Fatal("expected nil without a policy source")
	}
	if NewAntiSpamGuard(resolver, policies, nil) != nil {
		t.Fatal("expected nil without a limiter")
	}
}

// ── Concurrency ──────────────────────────────────────────────────────────────

// Concurrent sends in two workspaces must each respect their own budget, and
// neither may consume the other's. Synchronisation is by WaitGroup, not sleeps.
func TestAntiSpamGuard_ConcurrentSendsRespectPerWorkspaceBudgets(t *testing.T) {
	const perWorkspace = 20
	limiter := newCountingLimiter()
	policies := newStubPolicySource().set("ws-a", perWorkspace).set("ws-b", perWorkspace)

	// One guard per workspace sharing the limiter: two replicas, each serving a
	// workspace, hitting one Valkey.
	guardA, _ := newTestGuard(t, &stubWorkspaceResolver{id: "ws-a"}, policies, limiter)
	guardB, _ := newTestGuard(t, &stubWorkspaceResolver{id: "ws-b"}, policies, limiter)

	var accepted [2]int64
	var mu sync.Mutex
	var wg sync.WaitGroup
	start := make(chan struct{})

	send := func(guard *AntiSpamGuard, slot int) {
		defer wg.Done()
		<-start
		allowed, err := guard.Allow(context.Background(), "user-1", []string{"ws-a", "ws-b"}[slot])
		if err != nil {
			t.Errorf("Allow: %v", err)
			return
		}
		if allowed {
			mu.Lock()
			accepted[slot]++
			mu.Unlock()
		}
	}

	// Twice the budget in flight against each workspace.
	for range perWorkspace * 2 {
		wg.Add(2)
		go send(guardA, 0)
		go send(guardB, 1)
	}
	close(start)
	wg.Wait()

	if accepted[0] != perWorkspace {
		t.Fatalf("ws-a accepted %d, want exactly its budget of %d", accepted[0], perWorkspace)
	}
	if accepted[1] != perWorkspace {
		t.Fatalf("ws-b accepted %d, want exactly its budget of %d", accepted[1], perWorkspace)
	}
}

// ── Domain bounds ────────────────────────────────────────────────────────────

func TestValidMessageRateLimitPerMinute(t *testing.T) {
	tests := []struct {
		value int
		want  bool
	}{
		{value: 0, want: false},
		{value: -1, want: false},
		{value: domain.MinMessageRateLimitPerMinute, want: true},
		{value: domain.DefaultMessageRateLimitPerMinute, want: true},
		{value: domain.MaxMessageRateLimitPerMinute, want: true},
		{value: domain.MaxMessageRateLimitPerMinute + 1, want: false},
		{value: 1 << 30, want: false},
	}
	for _, tt := range tests {
		if got := domain.ValidMessageRateLimitPerMinute(tt.value); got != tt.want {
			t.Errorf("ValidMessageRateLimitPerMinute(%d) = %v, want %v", tt.value, got, tt.want)
		}
	}
}

func TestEffectiveMessageRateLimitPerMinute_NeverYieldsAnUnlimitedBudget(t *testing.T) {
	for _, value := range []int{0, -5, domain.MaxMessageRateLimitPerMinute + 1} {
		if got := domain.EffectiveMessageRateLimitPerMinute(value); got != domain.DefaultMessageRateLimitPerMinute {
			t.Errorf("EffectiveMessageRateLimitPerMinute(%d) = %d, want the default", value, got)
		}
	}
	if got := domain.EffectiveMessageRateLimitPerMinute(120); got != 120 {
		t.Errorf("a valid policy must be preserved, got %d", got)
	}
}

// ── Admin update ↔ cache invalidation ────────────────────────────────────────

// guardSettingsStub is a minimal storage.WorkspaceSettingsStore for driving the
// admin PATCH against a real guard.
type guardSettingsStub struct {
	perMinute      int
	maxUploadBytes int64
	updateErr      error
}

func (s *guardSettingsStub) GetWorkspaceByID(_ context.Context, id string) (domain.Workspace, error) {
	return domain.Workspace{
		ID: id, MessageRateLimitPerMinute: s.perMinute, MaxUploadBytes: s.maxUploadBytes,
	}, nil
}

func (s *guardSettingsStub) UpdateMaxUploadBytes(_ context.Context, workspaceID, _ string, maxBytes int64) (domain.Workspace, error) {
	if s.updateErr != nil {
		return domain.Workspace{}, s.updateErr
	}
	s.maxUploadBytes = maxBytes
	return domain.Workspace{ID: workspaceID, MaxUploadBytes: maxBytes}, nil
}

func (s *guardSettingsStub) UpdateEditWindow(_ context.Context, workspaceID, _ string, seconds *int) (domain.Workspace, error) {
	return domain.Workspace{ID: workspaceID, EditWindowSeconds: seconds}, nil
}

func (s *guardSettingsStub) UpdateMessageRateLimit(_ context.Context, workspaceID, _ string, perMinute int) (domain.Workspace, error) {
	if s.updateErr != nil {
		return domain.Workspace{}, s.updateErr
	}
	s.perMinute = perMinute
	return domain.Workspace{ID: workspaceID, MessageRateLimitPerMinute: perMinute}, nil
}

type guardAllowAuthorizer struct{}

func (guardAllowAuthorizer) CanManageWorkspace(context.Context, string, string) (bool, error) {
	return true, nil
}

func patchAntiSpamPolicy(t *testing.T, handler *MessageHandler, workspaceID, body string) int {
	t.Helper()
	request := httptest.NewRequest(http.MethodPatch, "/api/chat/workspaces/"+workspaceID+"/anti-spam",
		strings.NewReader(body))
	request = request.WithContext(context.WithValue(request.Context(), ctxKeyUserID,
		"44444444-4444-4444-4444-444444444444"))
	request.SetPathValue("workspaceID", workspaceID)
	recorder := httptest.NewRecorder()
	handler.UpdateWorkspaceAntiSpam(recorder, request)
	return recorder.Code
}

// A rejected write must leave the cache alone: invalidating on failure would
// force a re-read that returns the same value, and would advertise a change
// that never happened.
func TestAntiSpamGuard_FailedPolicyUpdateDoesNotInvalidateTheCache(t *testing.T) {
	const workspaceID = "11111111-1111-1111-1111-111111111111"
	policies := newStubPolicySource().set(workspaceID, 60)
	guard, _ := newTestGuard(t, &stubWorkspaceResolver{id: workspaceID}, policies, newCountingLimiter())
	handler := NewMessageHandler(nil, nil, nil).
		WithEditing(&guardSettingsStub{perMinute: 60, updateErr: errors.New("write failed")}, guardAllowAuthorizer{}, nil).
		WithAntiSpam(guard)

	guardedRequest(t, guard, &countingHandler{}, "user-1") // warm the cache

	if code := patchAntiSpamPolicy(t, handler, workspaceID, `{"message_rate_limit_per_minute": 30}`); code != http.StatusInternalServerError {
		t.Fatalf("expected the update to fail with 500, got %d", code)
	}

	guardedRequest(t, guard, &countingHandler{}, "user-1")
	if policies.readCount(workspaceID) != 1 {
		t.Fatalf("a failed update invalidated the cache: %d reads", policies.readCount(workspaceID))
	}
}

// A successful write invalidates that workspace's entry, so the new limit is in
// force on this instance without waiting out the TTL.
func TestAntiSpamGuard_SuccessfulPolicyUpdateInvalidatesOnlyThatWorkspace(t *testing.T) {
	const workspaceID = "11111111-1111-1111-1111-111111111111"
	const otherID = "22222222-2222-2222-2222-222222222222"
	limiter := newCountingLimiter()
	policies := newStubPolicySource().set(workspaceID, 60).set(otherID, 100)
	resolver := &stubWorkspaceResolver{id: workspaceID}
	guard, _ := newTestGuard(t, resolver, policies, limiter)
	handler := NewMessageHandler(nil, nil, nil).
		WithEditing(&guardSettingsStub{perMinute: 60}, guardAllowAuthorizer{}, nil).
		WithAntiSpam(guard)

	// Warm both workspaces.
	guardedRequest(t, guard, &countingHandler{}, "user-1")
	resolver.set(otherID, nil)
	guardedRequest(t, guard, &countingHandler{}, "user-1")

	policies.set(workspaceID, 5)
	if code := patchAntiSpamPolicy(t, handler, workspaceID, `{"message_rate_limit_per_minute": 5}`); code != http.StatusOK {
		t.Fatalf("expected 200, got %d", code)
	}

	resolver.set(workspaceID, nil)
	guardedRequest(t, guard, &countingHandler{}, "user-1")
	if got := limiter.budgetFor(workspaceID, "user-1"); got != 5 {
		t.Fatalf("the updated workspace kept budget %d, want 5", got)
	}
	// The other workspace was neither evicted nor re-read.
	if policies.readCount(otherID) != 1 {
		t.Fatalf("updating one workspace evicted another: %d reads", policies.readCount(otherID))
	}
}
