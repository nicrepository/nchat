package httpapi

import (
	"math"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/nicrepository/nchat/libs/go/platform/httputil"
)

type userRateLimitEntry struct {
	mu         sync.Mutex
	timestamps []time.Time
}

type UserRateLimiter struct {
	mu       sync.Mutex
	limit    int
	window   time.Duration
	now      func() time.Time
	entries  map[string]*userRateLimitEntry
	done     chan struct{}
	stopOnce sync.Once
}

func NewUserRateLimiter(limit int, window time.Duration) *UserRateLimiter {
	return newUserRateLimiter(limit, window, time.Now)
}

func newUserRateLimiter(limit int, window time.Duration, now func() time.Time) *UserRateLimiter {
	if limit <= 0 || window <= 0 || now == nil {
		panic("media rate limiter requires a positive limit, window, and clock")
	}
	limiter := &UserRateLimiter{
		limit: limit, window: window, now: now,
		entries: make(map[string]*userRateLimitEntry),
		done:    make(chan struct{}),
	}
	go limiter.gcLoop()
	return limiter
}

func (l *UserRateLimiter) Stop() {
	if l == nil {
		return
	}
	l.stopOnce.Do(func() { close(l.done) })
}

func (l *UserRateLimiter) Allow(userID string) bool {
	if l == nil || userID == "" {
		return true
	}
	now := l.now()
	cutoff := now.Add(-l.window)

	l.mu.Lock()
	entry := l.entries[userID]
	if entry == nil {
		entry = &userRateLimitEntry{}
		l.entries[userID] = entry
	}
	entry.mu.Lock()
	l.mu.Unlock()
	defer entry.mu.Unlock()
	keep := 0
	for keep < len(entry.timestamps) && !entry.timestamps[keep].After(cutoff) {
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

func (l *UserRateLimiter) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, ok := AuthenticatedPrincipal(r)
		if ok && !l.Allow(principal.UserID) {
			retryAfter := int(math.Ceil(l.window.Seconds()))
			w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
			httputil.WriteError(w, http.StatusTooManyRequests, httputil.ErrCodeRateLimited, "rate limit exceeded")
			return
		}
		next.ServeHTTP(w, r)
	})
}

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

func (l *UserRateLimiter) gc() {
	cutoff := l.now().Add(-l.window)
	l.mu.Lock()
	defer l.mu.Unlock()
	for userID, entry := range l.entries {
		entry.mu.Lock()
		active := len(entry.timestamps) > 0 &&
			entry.timestamps[len(entry.timestamps)-1].After(cutoff)
		entry.mu.Unlock()
		if !active {
			delete(l.entries, userID)
		}
	}
}
