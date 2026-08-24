package httpapi

import (
	"container/list"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/nicrepository/nchat/libs/go/platform/httputil"
)

const (
	rateLimiterBucketTTL  = time.Hour
	rateLimiterMaxBuckets = 10_000
)

// IPRateLimiter is a token bucket keyed by client IP, used on the session
// handshake — the one route reachable with only a chat access token, and
// therefore the one worth brute-forcing.
//
// It is in-memory and per-process: it slows a single replica down, it does not
// coordinate a cluster. That is a deliberate floor, not the ceiling; the
// gateway remains the place a distributed limit belongs.
//
// # Why a map and a list
//
// The map answers "what is this client's budget" in constant time. The list
// answers "who has gone longest without spending", also in constant time, which
// is the question that has to be cheap when the map is full — and it is full
// exactly when someone is spraying fresh keys at it, which is the worst moment
// to start scanning ten thousand entries under a mutex.
//
// Clearing the map instead would be worse than slow: one burst of throwaway
// addresses would forgive every real client's spent budget, making overflow the
// cheapest way past the limiter. Evicting the least recently used keeps the
// ceiling without turning the eviction into the bypass — a client in the middle
// of being limited is the most recently updated one, so it is the last
// candidate, never the first.
type IPRateLimiter struct {
	mu             sync.Mutex
	limitPerMinute float64
	burst          float64
	// buckets and lru describe the same set and are only ever mutated together
	// under mu. Every bucket holds the list element that carries its key, so a
	// bucket found through the map can be promoted or removed without a search.
	buckets        map[string]*ipBucket
	lru            *list.List
	trustedProxies []*net.IPNet
	now            func() time.Time
}

type ipBucket struct {
	tokens    float64
	updatedAt time.Time
	// element is this bucket's position in lru; its Value is the map key.
	element *list.Element
}

func NewIPRateLimiter(limitPerMinute int, burst int, trustedProxies []*net.IPNet) *IPRateLimiter {
	if limitPerMinute <= 0 {
		limitPerMinute = 30
	}
	if burst <= 0 {
		burst = 10
	}
	return &IPRateLimiter{
		limitPerMinute: float64(limitPerMinute),
		burst:          float64(burst),
		buckets:        make(map[string]*ipBucket),
		lru:            list.New(),
		trustedProxies: trustedProxies,
		now:            time.Now,
	}
}

func (l *IPRateLimiter) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !l.allow(httputil.ClientIP(r, l.trustedProxies)) {
			w.Header().Set("Retry-After", "60")
			httputil.WriteError(w, http.StatusTooManyRequests, httputil.ErrCodeRateLimited, "too many requests")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// Allow spends one token for an arbitrary key.
//
// Exported so a caller that is not an HTTP middleware can share this limiter
// rather than growing a second one. The active diagnostics of issue #582 are
// the caller: they are limited per administrator and integration, not per IP,
// because the thing worth bounding there is how often one operator can make
// this pod open outbound connections.
//
// The bucket semantics, the TTL and the LRU ceiling are the same for every key
// space, which is the point — one limiter, one set of properties to review.
func (l *IPRateLimiter) Allow(key string) bool {
	if l == nil {
		return true
	}
	return l.allow(key)
}

// allow spends one token for key, and reports whether there was one to spend.
//
// Every branch is constant time: a map lookup, a list splice, and at most one
// eviction of the list's tail.
func (l *IPRateLimiter) allow(key string) bool {
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()

	bucket, ok := l.buckets[key]
	if ok && now.Sub(bucket.updatedAt) > rateLimiterBucketTTL {
		// Expired: the client has been idle longer than any budget is worth
		// remembering, so it starts over rather than inheriting a stale one.
		// Dropping it here is the only sweep this limiter needs — an expired
		// bucket is either touched again (and reset here) or it drifts to the
		// tail and is evicted.
		l.removeLocked(key, bucket)
		ok = false
	}
	if !ok {
		l.evictIfFullLocked()
		// A brand new key must not be handed a full bucket and then spend it on
		// this same request without paying for it.
		l.buckets[key] = &ipBucket{
			tokens:    l.burst - 1,
			updatedAt: now,
			element:   l.lru.PushFront(key),
		}
		return true
	}

	elapsed := now.Sub(bucket.updatedAt).Minutes()
	bucket.tokens = minFloat(l.burst, bucket.tokens+elapsed*l.limitPerMinute)
	bucket.updatedAt = now
	l.lru.MoveToFront(bucket.element)
	if bucket.tokens < 1 {
		return false
	}
	bucket.tokens--
	return true
}

// evictIfFullLocked makes room for one new key, and only when the map is
// already at its ceiling.
//
// It removes the list's tail: the key nobody has spent a token on for the
// longest. One eviction is always enough because allow adds at most one key per
// call and prunes immediately before adding.
func (l *IPRateLimiter) evictIfFullLocked() {
	for len(l.buckets) >= rateLimiterMaxBuckets {
		oldest := l.lru.Back()
		if oldest == nil {
			return
		}
		key, _ := oldest.Value.(string)
		bucket, ok := l.buckets[key]
		if !ok {
			// Unreachable while the two structures are mutated together; the
			// list is still repaired rather than left describing a key the map
			// has lost, which would make the ceiling unenforceable.
			l.lru.Remove(oldest)
			continue
		}
		l.removeLocked(key, bucket)
	}
}

// removeLocked drops a bucket from both structures. They describe one set, so
// neither is ever updated alone.
func (l *IPRateLimiter) removeLocked(key string, bucket *ipBucket) {
	l.lru.Remove(bucket.element)
	delete(l.buckets, key)
}

func minFloat(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}
