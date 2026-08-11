package linkpreview

import (
	"sync"
	"time"
)

// maxCacheEntries bounds the cache. It is a count rather than a byte budget
// because every entry is a handful of truncated strings, so the count already
// bounds the memory: at the field ceilings an entry is a couple of kilobytes,
// which puts a full cache in the low megabytes.
//
// The bound is the point. Without it, a client feeding unique URLs would grow
// the map without limit, which is a cheaper denial of service than the fetches
// themselves.
const maxCacheEntries = 1024

// cache remembers preview outcomes for a bounded time.
//
// It is in-process, which is a known and accepted ceiling: N replicas keep N
// caches, so a URL may be fetched once per replica. The alternative is the
// shared Valkey the service only optionally has, and a cache that made the
// feature depend on a bus it can run without would be a worse trade. Nothing
// here is authoritative and nothing here is sensitive — every entry is derived
// from a public page and keyed by a URL the caller already knows.
type cache struct {
	mu         sync.Mutex
	entries    map[string]cacheEntry
	maxEntries int
	now        func() time.Time
}

type cacheEntry struct {
	preview   Preview
	err       error
	expiresAt time.Time
}

func newCache(maxEntries int, now func() time.Time) *cache {
	if maxEntries <= 0 {
		maxEntries = maxCacheEntries
	}
	if now == nil {
		now = time.Now
	}
	return &cache{entries: make(map[string]cacheEntry), maxEntries: maxEntries, now: now}
}

// get returns a live entry. An expired one is reported as a miss and removed,
// so a stale answer is never served and a key nobody asks for again does not
// need a sweep to disappear.
func (c *cache) get(key string) (cacheEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[key]
	if !ok {
		return cacheEntry{}, false
	}
	if !c.now().Before(entry.expiresAt) {
		delete(c.entries, key)
		return cacheEntry{}, false
	}
	return entry, true
}

// set stores an outcome under key for ttl.
//
// The key is the canonical URL in full, so two different URLs can never share
// an entry: there is no truncation, no hashing and no normalisation beyond what
// canonicalURL already applied to the value the request was made with. That is
// what makes cache poisoning by near-miss URLs impossible rather than unlikely.
func (c *cache) set(key string, preview Preview, err error, ttl time.Duration) {
	if ttl <= 0 {
		return
	}
	now := c.now()
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.entries) >= c.maxEntries {
		c.evict(now)
	}
	c.entries[key] = cacheEntry{preview: preview, err: err, expiresAt: now.Add(ttl)}
}

// evict makes room: expired entries first, and if that freed nothing, the entry
// closest to expiring.
//
// ponytail: O(n) over a map bounded at maxEntries, on writes only. An LRU with
// its own list is the upgrade if the cache ever grows large enough for the scan
// to show up in a profile.
func (c *cache) evict(now time.Time) {
	var (
		oldestKey string
		oldest    time.Time
		found     bool
	)
	for key, entry := range c.entries {
		if !now.Before(entry.expiresAt) {
			delete(c.entries, key)
			found = true
			continue
		}
		if oldestKey == "" || entry.expiresAt.Before(oldest) {
			oldestKey, oldest = key, entry.expiresAt
		}
	}
	if !found && oldestKey != "" {
		delete(c.entries, oldestKey)
	}
}
