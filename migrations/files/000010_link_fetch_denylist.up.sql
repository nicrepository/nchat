-- 000010_link_fetch_denylist.up.sql
-- RF-21 (issue #135, CQ-002): one durable answer to "may this deployment fetch
-- this URL?", shared by every component that could authorise one.
--
-- # The hole this closes
--
-- chat.link_scans and files.link_scans are independent authorities with
-- independent lifetimes, so this sequence was reachable:
--
--   T0  files.link_scans holds SAFE for X, with TTL remaining
--   T1  chat reconciliation proves X MALICIOUS and records it in chat.link_scans
--   T2  a preview request for X reads files.link_scans, still sees SAFE,
--       and performs an outbound fetch of a URL this deployment already knows
--       is malicious
--
-- Eventual consistency is not an acceptable answer here: the whole point of the
-- verdict is to prevent that one request. So a condemnation has to be durable
-- and visible to every fetch authoriser *before* the reconciliation that produced
-- it is considered complete.
--
-- # Why a denylist and not a shared verdict store
--
-- Only the restrictive half needs to be global. SAFE is a per-service, expiring
-- clearance and is fine where it is; MALICIOUS is a fact about the world that
-- must dominate everywhere and must never expire into permission. Sharing only
-- the restriction is the smallest change that makes the answer single, and it is
-- monotonic — insert-only, so there is no ordering in which a later SAFE
-- overwrites an earlier MALICIOUS.
--
-- # Why it lives in `files`
--
-- file-service is the only component that performs a server-side fetch, so the
-- authority for "may we fetch" belongs next to the thing it authorises. Both
-- runtimes already hold SELECT/INSERT/UPDATE/DELETE on every table in `auth`,
-- `chat` and `files` (scripts/db/grant-runtime.sql), so no new grant, no new
-- schema and no new role is introduced — which also means no rollout step can be
-- forgotten and leave a service unable to read its own guard.
--
-- # Rolling deployment
--
-- Migrations run before the RollingUpdate, so the table, its backfill and its
-- guard all exist before any pod of either version starts. Three things make the
-- old code safe against it:
--
--   1. the backfill below carries every condemnation already known into the new
--      table, so the authority is not empty on the first day;
--   2. the same backfill expires any conflicting live SAFE row, so an old
--      *reader* — whose gate is "done and not expired" — stops finding a
--      clearance immediately;
--   3. the guard trigger below refuses any write that would make a denylisted URL
--      authorise a fetch, so an old *worker* cannot recreate one.
--
-- Together those mean an old pod that has never heard of this table still cannot
-- fetch a URL the deployment has condemned.

BEGIN;

CREATE TABLE files.link_fetch_denylist (
    -- SHA-256 of the canonical URL, the same key files.link_scans uses. Computed
    -- by one shared function (urlsafety.URLDigest) so chat-service and
    -- file-service cannot disagree about what a URL hashes to — a digest
    -- mismatch would be a veto that silently never matches.
    url_digest BYTEA PRIMARY KEY,

    -- Kept for operators, who need to know which address a row is about when
    -- deciding whether to lift it. It is never used for matching.
    canonical_url TEXT NOT NULL,

    -- Which component observed the condemnation. A closed set, for diagnosis
    -- only; it grants nothing and is never a metric label.
    source TEXT NOT NULL,

    denied_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT link_fetch_denylist_digest_length_check CHECK (octet_length(url_digest) = 32),
    CONSTRAINT link_fetch_denylist_url_length_check CHECK (char_length(canonical_url) <= 2048),
    CONSTRAINT link_fetch_denylist_source_check CHECK (source IN ('chat', 'files'))
);

COMMENT ON TABLE files.link_fetch_denylist IS
    'RF-21: canonical URLs any NChat component proved malicious. Insert-only; a '
    'row here forbids every server-side fetch of that URL regardless of what any '
    'per-service verdict row says. Removing a row is a deliberate operator '
    'revalidation, never something the application does.';

-- ---------------------------------------------------------------------------
-- Backfill: the authority must not start empty
-- ---------------------------------------------------------------------------
--
-- Creating the table and leaving it empty would mean every condemnation reached
-- so far is invisible to it, and the invariant would only hold for URLs condemned
-- *after* the upgrade. The exact hole this feature exists to close would still be
-- open on the morning of the deploy:
--
--   chat.link_scans(X) = malicious   (already known)
--   files.link_scans(X) = done/safe  (still fresh)
--   -> a preview request for X fetches it
--
-- Both existing verdict stores are read. They key their rows differently — chat
-- by the canonical URL text, files by its SHA-256 — so the chat rows are hashed
-- on the way in. That the two digests agree is not assumed: PostgreSQL's
-- sha256(bytea) over the UTF-8 bytes of the canonical URL is exactly what
-- urlsafety.URLDigest computes, and TestLinkFetchDenylistBackfillPostgreSQL pins
-- it against the Go function for ASCII and non-ASCII URLs alike.
--
-- No canonicalisation is re-implemented here. Both tables already hold canonical
-- URLs, written by the Go canonicaliser; this only hashes what is there.

INSERT INTO files.link_fetch_denylist (url_digest, canonical_url, source)
SELECT sha256(canonical_url::bytea), canonical_url, 'chat'
FROM chat.link_scans
WHERE status = 'malicious'
  AND char_length(canonical_url) <= 2048
ON CONFLICT (url_digest) DO NOTHING;

INSERT INTO files.link_fetch_denylist (url_digest, canonical_url, source)
SELECT url_digest, canonical_url, 'files'
FROM files.link_scans
WHERE state = 'done' AND verdict = 'malicious'
  AND char_length(canonical_url) <= 2048
ON CONFLICT (url_digest) DO NOTHING;

-- MALICIOUS dominates SAFE, including retroactively. Any live clearance for a URL
-- that has just been denied is expired here, in the same transaction, so no
-- reader of either version finds one after this migration commits. Expiring
-- rather than deleting keeps the row's history and its scan uuid intact.
UPDATE files.link_scans ls
   SET verdict_expires_at = now(), updated_at = now()
 WHERE ls.state = 'done'
   AND ls.verdict = 'safe'
   AND ls.verdict_expires_at > now()
   AND EXISTS (
       SELECT 1 FROM files.link_fetch_denylist d WHERE d.url_digest = ls.url_digest
   );

-- ---------------------------------------------------------------------------
-- The guard: a denied URL can never be granted clearance again, by anyone
-- ---------------------------------------------------------------------------
--
-- New code consults the denylist on read, which protects new pods. It does
-- nothing for an old pod, whose reader does not know the table exists — so the
-- protection has to be on the *write* side, in the database, where every version
-- of the application is subject to it.
--
-- What is refused: any INSERT or UPDATE that would leave files.link_scans holding
-- a row that authorises a fetch of a denied URL — `state = 'done'`, `verdict =
-- 'safe'`, and an expiry still in the future. That is precisely the shape the
-- legacy reader's gate looks for, so a row that can never take it is a row that
-- can never grant clearance to any version.
--
-- What is deliberately allowed:
--
--   * done/malicious — the restrictive outcome, which grants nothing;
--   * expiring or withdrawing an existing clearance (verdict_expires_at set to
--     now or the past), which is what the backfill above and
--     InvalidateFetchAuthoritySQL both do. Blocking those would make the
--     invalidation impossible;
--   * every write for a URL that is not denied.
--
-- RETURN NULL rather than RAISE: a legacy worker that receives an exception may
-- not handle it, and taking a pod down is a worse failure than a write that
-- affects zero rows. Zero rows is what its own compare-and-set already treats as
-- "another worker got there first", which is a path it is written to survive. The
-- cost is that such a worker may re-poll the same scan until it is replaced —
-- wasteful for the length of a rollout, and never unsafe.
CREATE FUNCTION files.reject_denylisted_link_clearance()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    -- Only a row that would actually authorise a fetch is interesting. A verdict
    -- that is absent, malicious, or already expired grants nothing.
    IF NEW.state IS DISTINCT FROM 'done'
       OR NEW.verdict IS DISTINCT FROM 'safe'
       OR NEW.verdict_expires_at IS NULL
       OR NEW.verdict_expires_at <= now() THEN
        RETURN NEW;
    END IF;

    IF EXISTS (
        SELECT 1 FROM files.link_fetch_denylist d WHERE d.url_digest = NEW.url_digest
    ) THEN
        RETURN NULL;
    END IF;

    RETURN NEW;
END
$$;

-- Fires before the inconclusive terminal guard, by name, and independently of it:
-- that one governs which transitions may leave a terminal row, this one governs
-- which rows may grant clearance. Both must pass.
CREATE TRIGGER link_scans_denylist_clearance_guard
    BEFORE INSERT OR UPDATE ON files.link_scans
    FOR EACH ROW
    EXECUTE FUNCTION files.reject_denylisted_link_clearance();

COMMIT;
