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

// ── Hourly limiter (issue #425) ────────────────────────────────────────────

// The invite IP ceiling is stated per hour. Reusing the per-minute constructor
// would have made 30/hour into 30/minute — a sixty-fold weaker control.
func TestNewHourlyEndpointRateLimiter_AllowsTheWholeHourlyBudgetThenBlocks(t *testing.T) {
	limiter := NewHourlyEndpointRateLimiter(5, nil)

	for i := 0; i < 5; i++ {
		if !limiter.allow("198.51.100.7") {
			t.Fatalf("request %d of the hourly budget must be allowed", i+1)
		}
	}
	if limiter.allow("198.51.100.7") {
		t.Fatal("the request past the hourly budget must be rejected")
	}
}

// Budgets are per key: one caller exhausting theirs must not block another.
func TestNewHourlyEndpointRateLimiter_IsolatesKeys(t *testing.T) {
	limiter := NewHourlyEndpointRateLimiter(1, nil)

	if !limiter.allow("198.51.100.7") {
		t.Fatal("first caller must be allowed")
	}
	if limiter.allow("198.51.100.7") {
		t.Fatal("first caller must now be over budget")
	}
	if !limiter.allow("203.0.113.9") {
		t.Fatal("a different caller must have its own budget")
	}
}

func TestNewHourlyEndpointRateLimiter_NonPositiveFallsBackToDefault(t *testing.T) {
	if limiter := NewHourlyEndpointRateLimiter(0, nil); limiter == nil {
		t.Fatal("expected a limiter even for a non-positive budget")
	} else if !limiter.allow("198.51.100.7") {
		t.Fatal("the fallback budget must allow a first request")
	}
}
