package linkpreview

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

// fakeClock drives the TTL deterministically. Cache expiry tested with real
// sleeps is a slow test that fails on a loaded machine.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func TestCacheMissThenHit(t *testing.T) {
	clock := newClock()
	cache := newCache(8, clock.Now)

	if _, ok := cache.get("https://example.com/"); ok {
		t.Fatal("expected a miss on an empty cache")
	}
	cache.set("https://example.com/", Preview{Title: "cached"}, nil, time.Minute)

	entry, ok := cache.get("https://example.com/")
	if !ok {
		t.Fatal("expected a hit")
	}
	if entry.preview.Title != "cached" || entry.err != nil {
		t.Fatalf("unexpected entry %+v", entry)
	}
}

func TestCacheExpires(t *testing.T) {
	clock := newClock()
	cache := newCache(8, clock.Now)
	cache.set("https://example.com/", Preview{Title: "cached"}, nil, time.Minute)

	clock.advance(59 * time.Second)
	if _, ok := cache.get("https://example.com/"); !ok {
		t.Fatal("expected the entry to still be live before its TTL")
	}

	clock.advance(2 * time.Second)
	if _, ok := cache.get("https://example.com/"); ok {
		t.Fatal("expected the entry to be gone after its TTL")
	}
}

// TestCacheKeepsDistinctURLsApart is the cache-poisoning case: near-miss URLs
// must never read each other's entry.
func TestCacheKeepsDistinctURLsApart(t *testing.T) {
	clock := newClock()
	cache := newCache(64, clock.Now)
	urls := []string{
		"https://example.com/",
		"https://example.com/a",
		"https://example.com/a?b=1",
		"https://example.com/a?b=2",
		"http://example.com/a",
		"https://other.example.com/a",
		"https://example.com.evil.test/a",
	}
	for index, raw := range urls {
		cache.set(raw, Preview{Title: fmt.Sprintf("title-%d", index)}, nil, time.Minute)
	}
	for index, raw := range urls {
		entry, ok := cache.get(raw)
		if !ok {
			t.Fatalf("expected a hit for %q", raw)
		}
		if want := fmt.Sprintf("title-%d", index); entry.preview.Title != want {
			t.Fatalf("%q returned %q, want %q", raw, entry.preview.Title, want)
		}
	}
}

func TestCacheStoresFailures(t *testing.T) {
	clock := newClock()
	cache := newCache(8, clock.Now)
	cache.set("https://example.com/", Preview{}, ErrTimeout, negativeTTL)

	entry, ok := cache.get("https://example.com/")
	if !ok {
		t.Fatal("expected the failure to be cached")
	}
	if !errors.Is(entry.err, ErrTimeout) {
		t.Fatalf("unexpected error %v", entry.err)
	}
}

// TestCacheStaysBounded is the denial-of-service case: a client feeding unique
// URLs must not grow the map without limit.
func TestCacheStaysBounded(t *testing.T) {
	clock := newClock()
	cache := newCache(16, clock.Now)

	for index := range 1000 {
		cache.set(fmt.Sprintf("https://example.com/%d", index), Preview{}, nil, time.Minute)
	}
	cache.mu.Lock()
	size := len(cache.entries)
	cache.mu.Unlock()

	if size > 16 {
		t.Fatalf("cache grew to %d entries, cap is 16", size)
	}
}

// TestCacheEvictsExpiredBeforeLive: pressure must fall on entries that are
// already dead before it falls on useful ones.
func TestCacheEvictsExpiredBeforeLive(t *testing.T) {
	clock := newClock()
	cache := newCache(3, clock.Now)
	cache.set("https://example.com/short", Preview{}, nil, time.Second)
	cache.set("https://example.com/long", Preview{Title: "keep"}, nil, time.Hour)
	cache.set("https://example.com/other", Preview{Title: "keep"}, nil, time.Hour)

	clock.advance(2 * time.Second)
	cache.set("https://example.com/new", Preview{Title: "new"}, nil, time.Hour)

	if _, ok := cache.get("https://example.com/short"); ok {
		t.Fatal("expected the expired entry to be evicted")
	}
	for _, key := range []string{"https://example.com/long", "https://example.com/other",
		"https://example.com/new"} {
		if _, ok := cache.get(key); !ok {
			t.Fatalf("expected %q to survive eviction", key)
		}
	}
}

func TestCacheIgnoresNonPositiveTTL(t *testing.T) {
	clock := newClock()
	cache := newCache(8, clock.Now)
	cache.set("https://example.com/", Preview{Title: "x"}, nil, 0)

	if _, ok := cache.get("https://example.com/"); ok {
		t.Fatal("expected a zero TTL to store nothing")
	}
}

func TestCacheIsConcurrencySafe(t *testing.T) {
	cache := newCache(64, time.Now)
	var group sync.WaitGroup
	for worker := range 16 {
		group.Add(1)
		go func() {
			defer group.Done()
			for index := range 100 {
				key := fmt.Sprintf("https://example.com/%d/%d", worker, index)
				cache.set(key, Preview{Title: key}, nil, time.Minute)
				cache.get(key)
			}
		}()
	}
	group.Wait()
}
