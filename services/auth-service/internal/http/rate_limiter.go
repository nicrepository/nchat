package httpapi

import (
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/nicrepository/nchat/libs/go/platform/httputil"
)

const (
	fallbackTokenEndpointRateLimitPerMinute = 60
	fallbackTokenEndpointRateLimitBurst     = 10
)

type tokenEndpointRateLimiter struct {
	mu             sync.Mutex
	limitPerMinute float64
	burst          float64
	now            func() time.Time
	buckets        map[string]*tokenBucket
}

type tokenBucket struct {
	tokens    float64
	updatedAt time.Time
}

func NewTokenEndpointRateLimiter(limitPerMinute int, burst int) *tokenEndpointRateLimiter {
	if limitPerMinute <= 0 {
		limitPerMinute = fallbackTokenEndpointRateLimitPerMinute
	}
	if burst <= 0 {
		burst = fallbackTokenEndpointRateLimitBurst
	}
	return &tokenEndpointRateLimiter{
		limitPerMinute: float64(limitPerMinute),
		burst:          float64(burst),
		now:            time.Now,
		buckets:        make(map[string]*tokenBucket),
	}
}

func (l *tokenEndpointRateLimiter) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !l.allow(r.RemoteAddr) {
			httputil.WriteError(w, http.StatusTooManyRequests, httputil.ErrCodeRateLimited, "rate limit exceeded")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (l *tokenEndpointRateLimiter) allow(remoteAddr string) bool {
	key := remoteAddrKey(remoteAddr)
	now := l.now()

	l.mu.Lock()
	defer l.mu.Unlock()

	bucket, ok := l.buckets[key]
	if !ok {
		l.buckets[key] = &tokenBucket{tokens: l.burst - 1, updatedAt: now}
		return true
	}

	elapsedMinutes := now.Sub(bucket.updatedAt).Minutes()
	if elapsedMinutes > 0 {
		bucket.tokens += elapsedMinutes * l.limitPerMinute
		if bucket.tokens > l.burst {
			bucket.tokens = l.burst
		}
		bucket.updatedAt = now
	}
	if bucket.tokens < 1 {
		return false
	}
	bucket.tokens--
	return true
}

func remoteAddrKey(remoteAddr string) string {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil || host == "" {
		return remoteAddr
	}
	return host
}
