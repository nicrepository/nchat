package httpapi

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/nicrepository/nchat/libs/go/platform/httputil"
	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
)

// antiSpamWindowSeconds is the RF-19 window. The requirement is stated in
// messages per user per *minute*, so this is not configurable — only the number
// of messages allowed inside it is.
const antiSpamWindowSeconds = 60

// antiSpamPolicyTTL bounds how long an instance may keep serving a policy it
// read from PostgreSQL. It is the answer to "how long after an admin saves does
// the new limit take effect": immediately on the instance that served the
// PATCH (it invalidates that workspace's entry) and within this TTL everywhere
// else.
//
// Five seconds keeps the database read off the hot path — one query per
// workspace per five seconds instead of one per message — while staying short
// enough that an admin reacting to an incident does not wait on it.
const antiSpamPolicyTTL = 5 * time.Second

// antiSpamLimiter is the distributed Lua/Valkey limiter the whole service
// already shares (ws.ValkeyReactionLimiter). RF-19 does not introduce a second
// rate limiting mechanism: it feeds a per-workspace budget into this one.
//
// The counter therefore lives in Valkey, not in process memory, which is what
// makes the limit hold across chat-service replicas.
type antiSpamLimiter interface {
	AllowActionWithLimit(ctx context.Context, userID, action string, maxActions, windowSeconds int) (bool, error)
}

// canonicalWorkspaceResolver yields the workspace an authenticated request
// operates on, decided entirely server-side.
//
// This is the guard's *only* source of workspace identity. The guard itself has
// no notion of a default, a fallback, or a client-supplied ID: it asks this
// resolver, and a resolution failure is an error, never a substitution. None of
// the three send routes carries a workspace segment, so there is no path,
// query, body or header value that could reach it in the first place.
//
// It is satisfied by the same adapter ws.ServeWS already uses to bind a
// WebSocket session to its workspace, so session and send agree by construction.
type canonicalWorkspaceResolver interface {
	ResolveWorkspaceID(ctx context.Context) (string, error)
}

// antiSpamPolicySource loads a workspace's persisted policy *by its ID*.
// Loading by ID rather than by any notion of "the current workspace" is what
// makes a cache miss for one workspace incapable of answering with another's
// policy.
type antiSpamPolicySource interface {
	GetWorkspaceByID(ctx context.Context, id string) (domain.Workspace, error)
}

// cachedAntiSpamPolicy is one workspace's policy plus the moment it was read.
type cachedAntiSpamPolicy struct {
	perMinute int
	loadedAt  time.Time
}

// Sentinel errors distinguishing the ways enforcement can fail to decide.
// They never reach a response body — the middleware maps them to a status.
var (
	errAntiSpamWorkspaceUnresolved = errors.New("anti-spam: workspace could not be resolved")
	errAntiSpamPolicyUnavailable   = errors.New("anti-spam: policy unavailable")
)

// AntiSpamGuard enforces the RF-19 per-workspace, per-user message rate limit
// (issue #419) on every path that creates a message.
//
// It replaces the in-memory UserRateLimiter that previously guarded the send
// routes. That limiter counted per process, so N replicas granted a user N
// times the intended budget; the Valkey counter behind this guard is shared.
//
// Everything the guard does is scoped to one workspace ID that it is *given*:
// the policy is loaded by that ID, cached under that ID, and spent against a
// counter keyed by that ID. Two workspaces can therefore neither share a budget
// nor read each other's policy, and a workspace with no cached policy never
// borrows another's.
//
// Failure behaviour is deliberate and never silently disables the protection:
//
//   - Workspace unresolvable → 503. The send is refused. No default workspace
//     is substituted, so the request cannot be charged to someone else's budget
//     or judged by someone else's policy.
//   - PostgreSQL unreachable, this workspace's policy previously read → that
//     workspace's last known policy is reused past its TTL. Enforcement
//     continues at a possibly stale limit rather than stopping.
//   - PostgreSQL unreachable, nothing ever read for this workspace → 503. The
//     request is refused, not admitted unmetered.
//   - Valkey unreachable → 503, mirroring what EditMessage already does when
//     the same limiter errors. The message is neither persisted nor published,
//     because the guard runs before the handler.
//
// There is no configuration value that turns the guard off: domain bounds keep
// the policy in [1, 600], so "unlimited" is not expressible.
type AntiSpamGuard struct {
	workspaces canonicalWorkspaceResolver
	policies   antiSpamPolicySource
	limiter    antiSpamLimiter
	ttl        time.Duration
	// now is injectable so tests can advance the TTL without sleeping.
	now func() time.Time

	// mu guards cached. Entries are per workspace; workspaces are an
	// administrative resource created by operators, not by users, so the map is
	// bounded by how many exist and needs no eviction policy of its own.
	mu     sync.Mutex
	cached map[string]cachedAntiSpamPolicy
}

// NewAntiSpamGuard returns a guard resolving each request's workspace through
// workspaces, reading policy through policies and counting through limiter. All
// three are required; a nil argument yields nil, which the router turns into a
// refusal rather than an unguarded send path.
func NewAntiSpamGuard(workspaces canonicalWorkspaceResolver, policies antiSpamPolicySource, limiter antiSpamLimiter) *AntiSpamGuard {
	if workspaces == nil || policies == nil || limiter == nil {
		return nil
	}
	return &AntiSpamGuard{
		workspaces: workspaces,
		policies:   policies,
		limiter:    limiter,
		ttl:        antiSpamPolicyTTL,
		now:        time.Now,
		cached:     make(map[string]cachedAntiSpamPolicy),
	}
}

// Invalidate drops the cached policy for workspaceID and nothing else. Called
// by the admin PATCH handler after the new value is persisted: it makes the
// change take effect on this instance immediately instead of at the end of the
// TTL. Other instances still converge within the TTL, which is why this is an
// optimisation and not what "applies without restart" rests on.
//
// Only the named workspace's entry is removed, so an admin saving in one
// workspace cannot force every other workspace back to a database read, and
// cannot replace another workspace's cached policy with their own.
func (g *AntiSpamGuard) Invalidate(workspaceID string) {
	if g == nil || workspaceID == "" {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.cached, workspaceID)
}

// policy returns the effective per-minute limit for workspaceID.
//
// ok is false only when this workspace has no policy at all — neither fresh nor
// stale — which the caller turns into a refusal. A failure to read workspace A
// never resolves to workspace B's policy: both the lookup and the cache entry
// are keyed by the ID passed in.
func (g *AntiSpamGuard) policy(ctx context.Context, workspaceID string) (int, bool) {
	g.mu.Lock()
	entry, cached := g.cached[workspaceID]
	g.mu.Unlock()

	if cached && g.now().Sub(entry.loadedAt) < g.ttl {
		return entry.perMinute, true
	}

	workspace, err := g.policies.GetWorkspaceByID(ctx, workspaceID)
	if err != nil {
		// Stale beats unprotected — but only this workspace's own stale value.
		if cached {
			return entry.perMinute, true
		}
		return 0, false
	}

	// A row written before migration 000018, or any value outside the domain
	// bounds, resolves to the default — never to "no limit".
	perMinute := domain.EffectiveMessageRateLimitPerMinute(workspace.MessageRateLimitPerMinute)

	g.mu.Lock()
	g.cached[workspaceID] = cachedAntiSpamPolicy{perMinute: perMinute, loadedAt: g.now()}
	g.mu.Unlock()

	return perMinute, true
}

// Allow reports whether userID may send one more message in workspaceID right
// now, consuming a slot when they may. Rejected attempts still increment the
// underlying counter — that is the existing fixed-window limiter's semantics
// and RF-19 does not change it: a client that keeps hammering after a 429 stays
// blocked for the remainder of the window instead of being handed a free retry.
//
// workspaceID is mandatory. An empty value is an error and never a cue to fall
// back to some other workspace, which is what would let one workspace's traffic
// land in another's budget.
//
// The second return value distinguishes "over the limit" (false, nil) from
// "could not tell" (false, err), which the middleware maps to 429 and 503
// respectively.
func (g *AntiSpamGuard) Allow(ctx context.Context, userID, workspaceID string) (bool, error) {
	if workspaceID == "" {
		return false, errAntiSpamWorkspaceUnresolved
	}
	if userID == "" {
		return false, errAntiSpamWorkspaceUnresolved
	}
	perMinute, ok := g.policy(ctx, workspaceID)
	if !ok {
		return false, errAntiSpamPolicyUnavailable
	}
	// The workspace is part of the counter key, so the same user sending in two
	// workspaces spends two independent budgets. Channels and DMs deliberately
	// share one budget per workspace: the policy is about how much a person may
	// say, not about where.
	return g.limiter.AllowActionWithLimit(ctx, userID, "send_message:"+workspaceID, perMinute, antiSpamWindowSeconds)
}

// Middleware applies the RF-19 limit before next runs, so a rejected message is
// never persisted and never published.
//
// It resolves the request's canonical workspace once, server-side, and puts it
// in the request context. Downstream handlers reuse that value instead of
// resolving again, which is what makes it impossible for the workspace a
// message is charged to and the workspace it is written to to drift apart.
//
// Ordering requirement: it must sit after BearerAuth and RequireActiveSession,
// which is what guarantees a validated user ID is in context. A request with no
// user ID is passed through untouched — rate limiting an unauthenticated
// identity is meaningless and the auth middlewares upstream reject it anyway.
// This matches UserRateLimiter.Middleware.
//
// Retry-After is the fixed window length, as elsewhere in the service. The
// error code is the same "rate_limited" clients already handle, so no client
// change is needed for RF-19.
func (g *AntiSpamGuard) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		userID := GetContextUserID(r)
		if userID == "" {
			next.ServeHTTP(w, r)
			return
		}
		workspaceID, err := g.workspaces.ResolveWorkspaceID(r.Context())
		if err != nil || workspaceID == "" {
			// Refusing is the only safe answer: admitting the send would either
			// leave it uncounted or charge it to a workspace we did not verify.
			// The message body carries no detail about why.
			httputil.WriteError(w, http.StatusServiceUnavailable, "service_unavailable", "messages not available")
			return
		}
		ctx := context.WithValue(r.Context(), ctxKeyWorkspaceID, workspaceID)
		r = r.WithContext(ctx)

		allowed, err := g.Allow(ctx, userID, workspaceID)
		if err != nil {
			// Infrastructure failure. 503 keeps the protection on: the send is
			// refused rather than admitted without being counted.
			httputil.WriteError(w, http.StatusServiceUnavailable, "service_unavailable", "messages not available")
			return
		}
		if !allowed {
			w.Header().Set("Retry-After", "60")
			httputil.WriteError(w, http.StatusTooManyRequests, "rate_limited", "too many requests")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// antiSpamUnavailable is what the send routes serve when no guard was built —
// Valkey unconfigured at boot, so the shared counter does not exist.
//
// It answers 503 rather than falling back to a per-process limiter. A local
// fallback would give every replica its own full budget, which is precisely the
// cross-instance bypass RF-19 exists to close; degrading into it silently would
// be worse than refusing, and a chat-service without VALKEY_URL already fails
// its readiness probe (reaction-rate-limiter-configured) and receives no
// traffic in a cluster.
func antiSpamUnavailable() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		httputil.WriteError(w, http.StatusServiceUnavailable, "service_unavailable", "messages not available")
	})
}
