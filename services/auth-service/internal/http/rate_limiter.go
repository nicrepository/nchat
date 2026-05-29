package httpapi

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/nicrepository/nchat/libs/go/platform/httputil"
	"github.com/nicrepository/nchat/services/auth-service/internal/domain"
)

const (
	fallbackTokenEndpointRateLimitPerMinute = 60
	fallbackTokenEndpointRateLimitBurst     = 10
	defaultRateLimiterBucketTTL             = time.Hour
	defaultRateLimiterMaxBuckets            = 10_000
	defaultRateLimiterPruneInterval         = time.Minute
	minRateLimitSecretBytes                 = 32
	malformedTokenLimiterKey                = "malformed"
)

type tokenEndpointRateLimiter struct {
	mu                sync.Mutex
	limitPerMinute    float64
	burst             float64
	now               func() time.Time
	bucketTTL         time.Duration
	maxBuckets        int
	pruneInterval     time.Duration
	lastPrunedAt      time.Time
	buckets           map[string]*tokenBucket
	trustedProxyCIDRs []*net.IPNet
}

type tokenBucket struct {
	tokens    float64
	updatedAt time.Time
}

// NewTokenEndpointRateLimiter creates an in-memory token-bucket rate limiter.
//
// trustedProxyCIDRs is a comma-separated list of CIDR blocks (e.g.
// "10.0.0.0/8,172.16.0.0/12") whose X-Forwarded-For header is trusted for
// client-IP extraction. Leave empty (default) to always use RemoteAddr — the
// safe default for direct or single-instance deployments.
//
// WARNING: This limiter is in-memory and per-process. It does not protect
// multi-replica deployments; use a gateway or Valkey-based rate limit for
// production clusters.
func NewTokenEndpointRateLimiter(limitPerMinute int, burst int, trustedProxyCIDRs string) *tokenEndpointRateLimiter {
	if limitPerMinute <= 0 {
		limitPerMinute = fallbackTokenEndpointRateLimitPerMinute
	}
	if burst <= 0 {
		burst = fallbackTokenEndpointRateLimitBurst
	}
	return &tokenEndpointRateLimiter{
		limitPerMinute:    float64(limitPerMinute),
		burst:             float64(burst),
		now:               time.Now,
		bucketTTL:         defaultRateLimiterBucketTTL,
		maxBuckets:        defaultRateLimiterMaxBuckets,
		pruneInterval:     defaultRateLimiterPruneInterval,
		buckets:           make(map[string]*tokenBucket),
		trustedProxyCIDRs: parseCIDRs(trustedProxyCIDRs),
	}
}

func (l *tokenEndpointRateLimiter) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !l.allow(l.clientIP(r)) {
			writeRateLimited(w)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// clientIP returns the effective client IP for rate-limiting purposes.
// When RemoteAddr belongs to a configured trusted-proxy CIDR, the leftmost
// entry of X-Forwarded-For is used — but only after net.ParseIP validation
// and canonicalization (parsed.String()). X-Real-IP is the fallback when XFF
// is absent or its leftmost entry is not a valid IP. If no trusted-proxy CIDRs
// are configured, or if forwarded headers contain no valid IP, RemoteAddr is
// always used. Raw unvalidated header strings are never used as limiter keys.
func (l *tokenEndpointRateLimiter) clientIP(r *http.Request) string {
	remoteIP := remoteAddrKey(r.RemoteAddr)
	if len(l.trustedProxyCIDRs) == 0 {
		return remoteIP
	}
	ip := net.ParseIP(remoteIP)
	if ip == nil {
		return remoteIP
	}
	for _, cidr := range l.trustedProxyCIDRs {
		if cidr.Contains(ip) {
			// X-Forwarded-For: validate and canonicalize the leftmost entry only.
			if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
				first := strings.TrimSpace(strings.SplitN(xff, ",", 2)[0])
				if parsed := net.ParseIP(first); parsed != nil {
					return parsed.String()
				}
			}
			// X-Real-IP: validate and canonicalize.
			if xri := strings.TrimSpace(r.Header.Get("X-Real-IP")); xri != "" {
				if parsed := net.ParseIP(xri); parsed != nil {
					return parsed.String()
				}
			}
			break
		}
	}
	return remoteIP
}

// parseCIDRs parses a comma-separated list of CIDR strings.
// Invalid or empty entries are silently ignored.
func parseCIDRs(cidrs string) []*net.IPNet {
	var result []*net.IPNet
	for _, raw := range strings.Split(cidrs, ",") {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		_, ipNet, err := net.ParseCIDR(raw)
		if err == nil {
			result = append(result, ipNet)
		}
	}
	return result
}

func (l *tokenEndpointRateLimiter) allow(remoteAddr string) bool {
	key := remoteAddrKey(remoteAddr)
	now := l.now()

	l.mu.Lock()
	defer l.mu.Unlock()

	l.pruneExpiredBucketsLocked(now, false)
	bucket, ok := l.buckets[key]
	if !ok {
		l.evictBucketForCapacityLocked(now)
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

func (l *tokenEndpointRateLimiter) pruneExpiredBucketsLocked(now time.Time, force bool) {
	if l.bucketTTL <= 0 {
		return
	}
	if !force && !l.lastPrunedAt.IsZero() && l.pruneInterval > 0 && now.Sub(l.lastPrunedAt) < l.pruneInterval {
		return
	}
	cutoff := now.Add(-l.bucketTTL)
	for key, bucket := range l.buckets {
		if bucket.updatedAt.Before(cutoff) {
			delete(l.buckets, key)
		}
	}
	l.lastPrunedAt = now
}

func (l *tokenEndpointRateLimiter) evictBucketForCapacityLocked(now time.Time) {
	if l.maxBuckets <= 0 || len(l.buckets) < l.maxBuckets {
		return
	}
	l.pruneExpiredBucketsLocked(now, true)
	for len(l.buckets) >= l.maxBuckets {
		oldestKey := ""
		oldestFound := false
		var oldestAt time.Time
		for key, bucket := range l.buckets {
			if !oldestFound || bucket.updatedAt.Before(oldestAt) {
				oldestKey = key
				oldestAt = bucket.updatedAt
				oldestFound = true
			}
		}
		if !oldestFound {
			return
		}
		delete(l.buckets, oldestKey)
	}
}

func remoteAddrKey(remoteAddr string) string {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil || host == "" {
		return remoteAddr
	}
	return host
}

type rateLimitKeyer struct {
	key []byte
}

func newRateLimitKeyer(secret string) *rateLimitKeyer {
	if len([]byte(secret)) >= minRateLimitSecretBytes {
		return &rateLimitKeyer{key: []byte(secret)}
	}
	key := make([]byte, minRateLimitSecretBytes)
	if _, err := rand.Read(key); err != nil {
		panic("rate limiter random key unavailable")
	}
	return &rateLimitKeyer{key: key}
}

func (k *rateLimitKeyer) hmacKey(domainName, value string) string {
	mac := hmac.New(sha256.New, k.key)
	_, _ = mac.Write([]byte("nchat-rate-limit-v1:"))
	_, _ = mac.Write([]byte(domainName))
	_, _ = mac.Write([]byte(":"))
	_, _ = mac.Write([]byte(value))
	return hex.EncodeToString(mac.Sum(nil))
}

type targetAwareRateLimiter struct {
	limiter *tokenEndpointRateLimiter
	keyer   *rateLimitKeyer
	domain  string
}

func newTargetAwareRateLimiter(limitPerMinute int, burst int, keyer *rateLimitKeyer, domainName string) *targetAwareRateLimiter {
	return &targetAwareRateLimiter{
		limiter: NewTokenEndpointRateLimiter(limitPerMinute, burst, ""),
		keyer:   keyer,
		domain:  domainName,
	}
}

func (l *targetAwareRateLimiter) allowEmail(email string) bool {
	if l == nil {
		return true
	}
	normalized := domain.NormalizeEmail(email)
	return l.limiter.allow(l.keyer.hmacKey(l.domain, normalized))
}

func (l *targetAwareRateLimiter) allowToken(token string) bool {
	if l == nil {
		return true
	}
	trimmed := strings.TrimSpace(token)
	if !looksLikeOpaqueToken(trimmed) {
		return l.limiter.allow(l.keyer.hmacKey(l.domain, malformedTokenLimiterKey))
	}
	return l.limiter.allow(l.keyer.hmacKey(l.domain, trimmed))
}

func looksLikeOpaqueToken(token string) bool {
	if len(token) < 32 || len(token) > 128 {
		return false
	}
	for _, r := range token {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '-' || r == '_':
		default:
			return false
		}
	}
	return true
}

func writeRateLimited(w http.ResponseWriter) {
	httputil.WriteError(w, http.StatusTooManyRequests, httputil.ErrCodeRateLimited, "rate limit exceeded")
}
