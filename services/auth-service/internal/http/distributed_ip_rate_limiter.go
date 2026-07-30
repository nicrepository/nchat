package httpapi

import (
	"context"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/nicrepository/nchat/libs/go/platform/httputil"
	"github.com/nicrepository/nchat/services/auth-service/internal/storage"
)

// adminInviteIPLimiterNamespace keeps this counter apart from every other
// control sharing the same table. Without it a bootstrap guess and an invite
// request from one address would spend each other's allowance.
const adminInviteIPLimiterNamespace = "admin-invites-ip"

// DistributedRateLimiter charges one request against a counter shared by every
// replica. Satisfied by storage.PGXBootstrapAttemptStore.
type DistributedRateLimiter interface {
	Allow(ctx context.Context, req storage.DistributedRateLimitRequest) (storage.DistributedRateLimitResult, error)
}

// SharedRateLimitStore is what the router needs: one shared counter backing
// both the bootstrap guessing budget and the per-IP invite ceiling. They are
// separate controls with separate namespaces, but there is only one place the
// counts can live if they are to survive a restart, so the router takes one
// dependency rather than two views of the same table.
type SharedRateLimitStore interface {
	BootstrapAttemptRecorder
	DistributedRateLimiter
}

// DistributedIPRateLimitConfig is the budget, the shared counter behind it, and
// what the request's client IP is trusted to be.
type DistributedIPRateLimitConfig struct {
	Limiter           DistributedRateLimiter
	Namespace         string
	Limit             int
	Window            time.Duration
	TrustedProxyCIDRs []*net.IPNet
	// Now is injectable so a test can cross a window boundary without waiting
	// for one. Zero means time.Now.
	Now func() time.Time
}

// RateLimitByClientIP bounds requests per client IP using a counter held in
// PostgreSQL rather than in process memory.
//
// The in-process token bucket this replaces was honest about being a per-pod
// approximation, and on most endpoints that trade is fine. It is not fine here:
// invite creation is the one authenticated action that reaches out to a person
// who is not yet a user, so a ceiling that multiplies by the replica count and
// resets on every deploy is a ceiling that does not bound what it claims to.
// The authoritative per-(actor, workspace) budget still lives in the creating
// transaction — this is the complementary control, and now it holds across
// replicas too.
//
// Placement is inside the guard chain, after authentication and authorization:
// an unauthenticated caller is rejected by BearerAuth first and so cannot spend
// another tenant's IP allowance to deny them service.
//
// On refusal: 429 with Retry-After set to the remainder of the window, and the
// handler is never called — no invite, no outbox entry, and no charge against
// the (actor, workspace) budget, which would otherwise let a throttled caller
// burn their own quota.
//
// It fails closed. If the counter is unreachable the request is refused with
// 503 rather than admitted: falling back to a per-process count would silently
// restore the very hole this exists to close, and quietly allowing the request
// would make the ceiling advisory.
func RateLimitByClientIP(cfg DistributedIPRateLimitConfig) func(http.Handler) http.Handler {
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if cfg.Limiter == nil || cfg.Limit <= 0 || cfg.Window <= 0 {
				// Unwired or misconfigured: refuse rather than serve an
				// endpoint whose advertised ceiling is not actually running.
				httputil.WriteError(w, http.StatusServiceUnavailable, errCodeUnavailable, "invite endpoint unavailable")
				return
			}

			result, err := cfg.Limiter.Allow(r.Context(), storage.DistributedRateLimitRequest{
				Namespace: cfg.Namespace,
				// The shared helper resolves the address from RemoteAddr,
				// consulting X-Forwarded-For only when the peer is a trusted
				// proxy, and strips the port. Only headers from a trusted peer
				// reach the key, so a client cannot mint budgets by varying one.
				Subject: canonicalIPKey(httputil.ClientIP(r, cfg.TrustedProxyCIDRs)),
				Limit:   cfg.Limit,
				Window:  cfg.Window,
				Now:     now(),
			})
			if err != nil {
				httputil.WriteError(w, http.StatusServiceUnavailable, errCodeUnavailable, "invite endpoint unavailable")
				return
			}
			if !result.Allowed {
				if seconds := int(result.RetryAfter.Seconds()); seconds > 0 {
					w.Header().Set("Retry-After", strconv.Itoa(seconds))
				}
				writeRateLimited(w)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// canonicalIPKey collapses the spellings of one address to a single counter
// key. net.IP.String renders an IPv4-mapped IPv6 address as its dotted quad and
// lower-cases and compresses IPv6, so a client cannot get a second budget by
// connecting over a different address family or writing the same address a
// different way.
//
// An address that will not parse is used verbatim rather than dropped: it still
// spends budget, so a malformed peer is never a way past the ceiling. It cannot
// inflate cardinality either — the value comes from RemoteAddr, or from a
// header only when the peer is a trusted proxy.
func canonicalIPKey(address string) string {
	if parsed := net.ParseIP(address); parsed != nil {
		return parsed.String()
	}
	return address
}
