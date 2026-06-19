package httpapi

import (
	"net/http"
	"sync"
	"time"

	"github.com/nicrepository/nchat/libs/go/platform/httputil"
)

// userRLEntry tracks per-user request timestamps within the current window.
type userRLEntry struct {
	mu         sync.Mutex
	timestamps []time.Time
}

// UserRateLimiter is a simple in-memory sliding-window rate limiter keyed by
// authenticated user ID. It is safe for concurrent use.
//
// The sliding window avoids the burst-at-boundary problem of fixed windows:
// only requests in the last [window] duration are counted.
//
// Lock ordering: always acquire l.mu before entry.mu. The reverse is never
// done, so there is no deadlock risk.
//
// A background GC goroutine (started by NewUserRateLimiter) evicts stale entries
// to bound memory growth. Call Stop() when the limiter is no longer needed.
type UserRateLimiter struct {
	mu      sync.Mutex
	limit   int
	window  time.Duration
	entries map[string]*userRLEntry
	done    chan struct{}
}

// NewUserRateLimiter creates a limiter allowing at most limit requests per
// window per user. Starts a background GC goroutine; call Stop() to halt it.
// Panics if limit <= 0 or window <= 0.
func NewUserRateLimiter(limit int, window time.Duration) *UserRateLimiter {
	if limit <= 0 {
		panic("ratelimit: limit must be > 0")
	}
	if window <= 0 {
		panic("ratelimit: window must be > 0")
	}
	l := &UserRateLimiter{
		limit:   limit,
		window:  window,
		entries: make(map[string]*userRLEntry),
		done:    make(chan struct{}),
	}
	go l.gcLoop()
	return l
}

// Stop halts the background GC goroutine. Call it when the limiter is no
// longer needed — e.g., in tests via t.Cleanup(limiter.Stop). Stop must not
// be called more than once.
func (l *UserRateLimiter) Stop() { close(l.done) }

// gcLoop runs a periodic sweep that removes entries whose timestamp slice has
// been empty for at least one full window. This bounds memory growth to active
// users only.
func (l *UserRateLimiter) gcLoop() {
	ticker := time.NewTicker(l.window)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			l.gc()
		case <-l.done:
			return
		}
	}
}

// gc evicts per-user entries that have no timestamps in the current window.
func (l *UserRateLimiter) gc() {
	cutoff := time.Now().Add(-l.window)
	l.mu.Lock()
	defer l.mu.Unlock()
	for userID, entry := range l.entries {
		entry.mu.Lock()
		i := 0
		for i < len(entry.timestamps) && entry.timestamps[i].Before(cutoff) {
			i++
		}
		active := len(entry.timestamps) - i
		entry.mu.Unlock()
		if active == 0 {
			delete(l.entries, userID)
		}
	}
}

// Allow reports whether userID may proceed under the current rate limit.
// It records the current request timestamp; call only when you intend to serve.
func (l *UserRateLimiter) Allow(userID string) bool {
	l.mu.Lock()
	entry, ok := l.entries[userID]
	if !ok {
		entry = &userRLEntry{}
		l.entries[userID] = entry
	}
	l.mu.Unlock()

	now := time.Now()
	cutoff := now.Add(-l.window)

	entry.mu.Lock()
	defer entry.mu.Unlock()

	// Prune timestamps outside the sliding window and compact the backing slice
	// to avoid unbounded memory retention after a burst.
	keep := 0
	for keep < len(entry.timestamps) && entry.timestamps[keep].Before(cutoff) {
		keep++
	}
	if keep > 0 {
		entry.timestamps = append(entry.timestamps[:0], entry.timestamps[keep:]...)
	}

	if len(entry.timestamps) >= l.limit {
		return false
	}
	entry.timestamps = append(entry.timestamps, now)
	return true
}

// Middleware wraps h and rejects requests exceeding the per-user rate limit
// with 429 Too Many Requests. It applies to every request, including initial
// page loads — protecting the database against flood reads whether or not the
// client supplies a pagination cursor.
//
// Budget is shared across channel and DM listing routes: a user paging
// aggressively through both simultaneously exhausts a single counter. This is
// intentional — the limit guards against DB read abuse regardless of which
// conversation is being listed.
//
// Retry-After is set to a fixed 60 s (the window size). This is conservative
// by design: the window slides and a slot may free sooner, but advertising the
// upper bound is simpler and avoids leaking internal timing details.
//
// Ordering requirement: this middleware must be placed after BearerAuth and
// RequireActiveSession in the handler chain. Those middlewares guarantee that
// GetContextUserID returns a validated, non-empty userID by the time Middleware
// runs. Requests that arrive without a userID in context (unauthenticated) are
// passed through — the auth middlewares upstream are responsible for rejecting
// them; rate limiting unauthenticated identities is not meaningful here.
func (l *UserRateLimiter) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		userID := GetContextUserID(r)
		if userID != "" && !l.Allow(userID) {
			// RFC 9110 §15.5.30: include Retry-After so clients know when to retry
			// rather than hammering the server in a tight loop on 429.
			w.Header().Set("Retry-After", "60")
			httputil.WriteError(w, http.StatusTooManyRequests, "rate_limited", "too many requests")
			return
		}
		next.ServeHTTP(w, r)
	})
}
