package httpapi

import (
	"strconv"
	"testing"
)

// The limiter is only interesting under pressure: a full map is exactly the
// state an attacker spraying fresh addresses produces, and it used to be the
// state where every request paid a full scan.
//
// These measure throughput, nothing more. A Go benchmark cannot demonstrate an
// asymptotic bound; what it can do is catch a change that makes the hot path
// grossly slower, which is the regression worth guarding.

func fillToCapacity(limiter *IPRateLimiter) {
	for i := 0; i < rateLimiterMaxBuckets; i++ {
		limiter.allow("seed-" + strconv.Itoa(i))
	}
}

// BenchmarkIPRateLimiterAtCapacityHit: an existing client spends a token while
// the map is full. Map lookup plus a list splice.
func BenchmarkIPRateLimiterAtCapacityHit(b *testing.B) {
	limiter := NewIPRateLimiter(600_000, 1_000_000, nil)
	fillToCapacity(limiter)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		limiter.allow("seed-0")
	}
}

// BenchmarkIPRateLimiterAtCapacityInsert: a brand new client arrives while the
// map is full, so every iteration evicts the tail and inserts at the head.
func BenchmarkIPRateLimiterAtCapacityInsert(b *testing.B) {
	limiter := NewIPRateLimiter(600_000, 1_000_000, nil)
	fillToCapacity(limiter)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		limiter.allow("fresh-" + strconv.Itoa(i))
	}
	b.StopTimer()
	if len(limiter.buckets) > rateLimiterMaxBuckets {
		b.Fatalf("the map must stay bounded, got %d", len(limiter.buckets))
	}
}

// BenchmarkIPRateLimiterEmptyInsert is the same insert with room to spare, so
// the difference between the two is the cost eviction adds.
func BenchmarkIPRateLimiterEmptyInsert(b *testing.B) {
	limiter := NewIPRateLimiter(600_000, 1_000_000, nil)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		limiter.allow("fresh-" + strconv.Itoa(i))
	}
}
