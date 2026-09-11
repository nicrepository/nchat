/**
 * A set of opaque keys that expire, bounded by capacity as well as by time.
 *
 * Extracted so the two dedupe owners in the chat client share one
 * implementation instead of each growing its own: realtimeMessageLedger (state
 * ingestion) and notificationBurst (presentation). They keep separate
 * instances with separate lifetimes — the structure is shared, the memory
 * never is.
 *
 * Only keys are stored, mapped to an instant. Nothing derived from a message
 * body, a sender or a conversation name ever enters here, so the whole of what
 * a leak of this state could disclose is that some id was seen.
 *
 * Invariant: `Map` iterates in insertion order and `add` re-inserts, so the
 * first key the iterator yields is always the least recently added — which is
 * what makes eviction a single O(1) delete rather than a scan for a victim.
 * Expiry is applied lazily on read; an expired key that is never read again
 * costs one slot until capacity reclaims it, and nothing sweeps in between. No
 * timer is created, ever, for any key.
 */
export interface ExpiringKeySet {
  has(key: string, now: number): boolean;
  add(key: string, now: number, ttlMs: number): void;
  size(): number;
}

export function createExpiringKeySet(capacity: number): ExpiringKeySet {
  const expiryByKey = new Map<string, number>();
  return {
    has(key: string, now: number): boolean {
      const expiresAt = expiryByKey.get(key);
      if (expiresAt === undefined) return false;
      if (expiresAt > now) return true;
      expiryByKey.delete(key);
      return false;
    },
    add(key: string, now: number, ttlMs: number): void {
      expiryByKey.delete(key);
      expiryByKey.set(key, now + ttlMs);
      if (expiryByKey.size <= capacity) return;
      const oldest = expiryByKey.keys().next();
      if (!oldest.done) expiryByKey.delete(oldest.value);
    },
    size(): number {
      return expiryByKey.size;
    },
  };
}
