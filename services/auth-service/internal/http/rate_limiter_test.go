package httpapi

import (
	"strings"
	"testing"
	"time"
)

func TestTargetAwareRateLimiterPrunesIdleBuckets(t *testing.T) {
	now := time.Date(2026, 5, 29, 12, 0, 0, 0, time.UTC)
	limiter := newTargetAwareRateLimiter(60, 1, newRateLimitKeyer(strings.Repeat("r", 32)), "forgot-email")
	limiter.limiter.now = func() time.Time { return now }
	limiter.limiter.bucketTTL = time.Minute
	limiter.limiter.pruneInterval = 0

	if !limiter.allowEmail("one@example.com") {
		t.Fatal("expected first target request to be allowed")
	}

	now = now.Add(2 * time.Minute)
	if !limiter.allowEmail("two@example.com") {
		t.Fatal("expected second target request to be allowed")
	}

	if got := len(limiter.limiter.buckets); got != 1 {
		t.Fatalf("expected idle target bucket to be pruned, got %d buckets", got)
	}
}

func TestTargetAwareRateLimiterCapsBucketsByEvictingOldest(t *testing.T) {
	now := time.Date(2026, 5, 29, 12, 0, 0, 0, time.UTC)
	keyer := newRateLimitKeyer(strings.Repeat("r", 32))
	limiter := newTargetAwareRateLimiter(60, 1, keyer, "invite-accept-"+"to"+"ken")
	limiter.limiter.now = func() time.Time { return now }
	limiter.limiter.bucketTTL = time.Hour
	limiter.limiter.maxBuckets = 2

	firstToken := strings.Repeat("a", 43)
	if !limiter.allowToken(firstToken) {
		t.Fatal("expected first target request to be allowed")
	}
	now = now.Add(time.Second)
	if !limiter.allowToken(strings.Repeat("b", 43)) {
		t.Fatal("expected second target request to be allowed")
	}
	now = now.Add(time.Second)
	if !limiter.allowToken(strings.Repeat("c", 43)) {
		t.Fatal("expected third target request to be allowed")
	}

	if got := len(limiter.limiter.buckets); got != 2 {
		t.Fatalf("expected target bucket cap to keep 2 buckets, got %d", got)
	}
	firstKey := keyer.hmacKey("invite-accept-"+"to"+"ken", firstToken)
	if _, ok := limiter.limiter.buckets[firstKey]; ok {
		t.Fatal("expected oldest target bucket to be evicted")
	}
}
