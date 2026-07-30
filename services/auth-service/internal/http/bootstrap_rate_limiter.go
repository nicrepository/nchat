package httpapi

import (
	"context"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/nicrepository/nchat/libs/go/platform/httputil"
)

const (
	// Namespace so the counter cannot collide with another limiter sharing the
	// table later. The credential is never part of a key: keying by what is
	// being guessed would give each guess its own budget.
	bootstrapLimiterNamespace = "bootstrap-admin-token"

	// A credential is 43 characters. Anything beyond this is not a truncated
	// token, it is someone probing what the server will buffer, and it is
	// rejected before the value is read into anything.
	maxAdminTokenHeaderBytes = 256
)

// BootstrapAttemptRecorder charges one attempt against key and reports whether
// it stayed inside the budget. Satisfied by storage.PGXBootstrapAttemptStore.
type BootstrapAttemptRecorder interface {
	RecordAttempt(ctx context.Context, key string, limit int, window time.Duration) (bool, error)
}

// BootstrapRateLimitConfig is the budget and the shared counter behind it.
type BootstrapRateLimitConfig struct {
	Recorder          BootstrapAttemptRecorder
	Attempts          int
	Window            time.Duration
	TrustedProxyCIDRs []*net.IPNet
}

// RateLimitBootstrapAttempts bounds guesses at the bootstrap credential, before
// the credential is looked at.
//
// Ordering is the point. AdminBootstrapGuard compares a pre-shared secret that
// can mint an invite conferring workspace ownership; with nothing in front of
// it, an attacker gets unlimited online guesses. This middleware must therefore
// wrap the guard, not follow it — every request spends budget before any
// comparison happens, so a wrong credential and a right one are equally
// expensive to try.
//
// Consequences of that position:
//
//   - the budget is charged on every attempt, valid or not, and a correct
//     credential neither resets nor refunds it. A stolen credential being
//     replayed is exactly the case where the limit should still apply;
//   - a rejected request never reaches the guard, so it never reads an invite,
//     writes an outbox row or touches the bootstrap lifecycle;
//   - the response is identical whether the credential was right, wrong or
//     absent — 429 says only that too many attempts were made from this
//     address.
//
// The counter is shared through PostgreSQL rather than held per process: an
// attacker must not get one budget per replica, nor a fresh one per restart.
//
// It fails closed. If the counter is unreachable the request is refused with
// 503 and the guard is not called: an unbounded credential check is a worse
// outcome than a bootstrap endpoint that is briefly unavailable, and falling
// back to a per-process count would silently restore the multi-replica hole
// this exists to close.
func RateLimitBootstrapAttempts(cfg BootstrapRateLimitConfig) func(http.Handler) http.Handler {
	retryAfterSeconds := int(cfg.Window.Seconds())
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if cfg.Recorder == nil || cfg.Attempts <= 0 || cfg.Window <= 0 {
				// Unwired or misconfigured: refuse rather than pass through to
				// an unbounded credential check.
				writeBootstrapLimiterUnavailable(w)
				return
			}

			// Oversized header first, and without reading the value: it is
			// rejected generically, but only after spending budget, so padding
			// the header is not a way to probe for free.
			oversized := len(r.Header.Get(adminTokenHeader)) > maxAdminTokenHeaderBytes

			key := bootstrapLimiterNamespace + ":" + httputil.ClientIP(r, cfg.TrustedProxyCIDRs)
			allowed, err := cfg.Recorder.RecordAttempt(r.Context(), key, cfg.Attempts, cfg.Window)
			if err != nil {
				writeBootstrapLimiterUnavailable(w)
				return
			}
			if !allowed {
				if retryAfterSeconds > 0 {
					w.Header().Set("Retry-After", strconv.Itoa(retryAfterSeconds))
				}
				writeRateLimited(w)
				return
			}
			if oversized {
				httputil.WriteError(w, http.StatusUnauthorized, httputil.ErrCodeUnauthorized, "unauthorized")
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

func writeBootstrapLimiterUnavailable(w http.ResponseWriter) {
	httputil.WriteError(w, http.StatusServiceUnavailable, errCodeUnavailable, "bootstrap endpoint unavailable")
}
