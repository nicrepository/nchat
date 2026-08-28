-- 000012_link_reconcile_leases.up.sql
-- RF-21 (issue #135, CQ-003): one cross-service cooldown on re-reading a URL at
-- the provider.
--
-- # The hole this closes
--
-- chat-service and file-service each hold their own inconclusive rows for the
-- same address, keyed differently (chat by canonical URL text, files by its
-- SHA-256), and each runs its own reconciliation pass on its own schedule. The
-- per-service cooldowns are therefore invisible to one another, so this is
-- reachable inside a single minute:
--
--   T0   chat's pass claims X, calls Search, then GET on the returned scan
--   T0   files' pass claims the same X, calls Search, then GET
--
-- Two searches and two report reads for one URL, on one account, answering one
-- question. The provider bills both, its own per-hostname limits count both, and
-- the second answer is a copy of the first — reconciliation reads a *finished*
-- scan, so nothing can have changed between them.
--
-- # Why a lease and not another use of the denylist
--
-- The denylist is monotonic and insert-only on purpose: a row there is a
-- permanent condemnation. A cooldown is the opposite — it is temporary, it
-- expires, and it grants nothing. Storing "we looked recently" next to "this is
-- malicious forever" would put an expiring row in the one table whose whole
-- guarantee is that its rows never expire into permission, and the first
-- operator to clear a stale cooldown would be one typo from clearing a
-- condemnation. Separate tables, separate lifetimes.
--
-- # Why keyed by url_digest
--
-- It is the only key both services can compute for the same address without
-- agreeing on a text representation, and it is already the key of both
-- files.link_scans and files.link_fetch_denylist. chat-service hashes its
-- canonical URL with the same function (urlsafety.URLDigest / PostgreSQL's
-- sha256 over the UTF-8 bytes), which is pinned against the Go implementation by
-- the cross-service tests.
--
-- # Why in `files`, and why no new grant
--
-- Same reasoning as 000010: both runtimes already hold full DML on every table
-- in `files` (scripts/db/grant-runtime.sql), so this introduces no rollout step
-- that could be forgotten and leave a service unable to take its own lease.
--
-- # Rolling deployment
--
-- The table is new and empty and nothing reads it except the code added in the
-- same change. An old pod takes no lease and is not blocked by one — during a
-- rollout the guarantee degrades to exactly today's behaviour, which is the
-- behaviour being fixed, and never to anything worse.

BEGIN;

CREATE TABLE files.link_reconcile_leases (
    -- SHA-256 of the canonical URL. One row per address, held by whichever
    -- service got there first.
    url_digest BYTEA PRIMARY KEY,

    -- For operators reading the table. Never used for matching, and overwritten
    -- by whichever service last took the lease.
    canonical_url TEXT NOT NULL,

    -- When the holder's exclusive window ends. A row whose lease_until is in the
    -- past is not deleted; it is simply available, and the next acquisition
    -- overwrites it. That keeps acquisition a single upsert with no cleanup pass
    -- and no window in which a deleted row races a new one.
    leased_until TIMESTAMPTZ NOT NULL,

    -- Which service holds it. Diagnosis only: it grants nothing, gates nothing,
    -- and is never a metric label.
    leased_by TEXT NOT NULL,

    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT link_reconcile_leases_digest_length_check CHECK (octet_length(url_digest) = 32),
    CONSTRAINT link_reconcile_leases_url_length_check CHECK (char_length(canonical_url) <= 2048),
    CONSTRAINT link_reconcile_leases_holder_check CHECK (leased_by IN ('chat', 'files'))
);

COMMENT ON TABLE files.link_reconcile_leases IS
    'RF-21: short-lived cross-service cooldown on re-reading one URL at the URL '
    'safety provider. Holding a row grants the exclusive right to spend a '
    'provider attempt on that URL until leased_until; it authorises nothing '
    'else, and an expired row is simply available.';

-- Expired rows are the common case and are never interesting; this keeps the
-- reclaim scan off the ones still held.
CREATE INDEX link_reconcile_leases_expiry_idx
    ON files.link_reconcile_leases (leased_until);

COMMIT;
