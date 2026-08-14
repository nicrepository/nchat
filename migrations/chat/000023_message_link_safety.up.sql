-- 000023_message_link_safety.up.sql
-- RF-21 (issue #135): hold a message carrying an unscanned link until Cloudflare
-- URL Scanner has answered about every URL in it.
--
-- Why the message gets a state instead of the send getting a wait:
--
--   URL Scanner is submit-then-poll. Its result endpoint answers 404 while a
--   scan runs and Cloudflare recommends polling every 10-30 seconds, so the
--   verdict for a URL nobody has scanned before simply cannot be obtained inside
--   an interactive request. The two honest options are to publish first and
--   check later, or to accept the message and withhold it. RF-21 is a blocking
--   control, so it is the second: the row exists, the sender can see their own,
--   and nobody else is shown it until the scan clears.
--
-- Why a new status value and not a boolean column:
--
--   Every read path in this service already filters `status = 'active'` — list,
--   get, search, quote preview, unread counts. A new status is therefore
--   *withholding by construction*: a pending message is invisible to every one
--   of those without a single one of them being edited, and a query added later
--   inherits the same behaviour instead of having to remember a flag. A boolean
--   beside `status = 'active'` would have to be added to each of them by hand,
--   and the one that was forgotten is the one that leaks the message.
--
-- The state machine has exactly two verdict transitions, mirroring RF-22's:
--
--   pending_link_scan --(all urls safe)-----> active
--   pending_link_scan --(any url malicious)-> deleted
--
-- A blocked message becomes `deleted` rather than gaining a third terminal
-- state: it was never published, so to every reader it is a message that does
-- not exist, which is exactly what `deleted` already means to them. The sender
-- is told why by the API, not by a status nobody else may read.

BEGIN;

ALTER TABLE chat.messages
    DROP CONSTRAINT messages_status_check;

ALTER TABLE chat.messages
    ADD CONSTRAINT messages_status_check
    CHECK (status IN ('active', 'deleted', 'pending_link_scan'));

-- ---------------------------------------------------------------------------
-- link_scans: one row per canonical URL, shared by every message that names it
-- ---------------------------------------------------------------------------
--
-- The primary key is the canonical URL itself and not a hash of it, because the
-- worker has to submit the URL to the provider after a restart — a hash would
-- mean storing the URL beside it anyway. Canonicalization (scheme, host, path,
-- query; fragment dropped) happens in libs/go/platform/urlsafety and is the only
-- thing allowed to decide that two spellings are the same resource.
--
-- Keying by URL rather than by message is what deduplicates the scans: ten
-- people pasting the same link produce one submission, and the eleventh gets the
-- verdict from this table without touching Cloudflare at all.
CREATE TABLE chat.link_scans (
    canonical_url    TEXT        PRIMARY KEY,
    -- pending: submitted or awaiting submission; safe/malicious: the provider
    -- answered. There is deliberately no 'failed' state — a failure leaves the
    -- row pending with a later next_attempt_at, because a URL nobody could scan
    -- is a URL that is still not cleared.
    status           TEXT        NOT NULL DEFAULT 'pending',
    -- The provider's scan id, once submitted. NULL means the submission has not
    -- happened or did not succeed, and the next claim will submit.
    scan_uuid        TEXT,
    attempts         SMALLINT    NOT NULL DEFAULT 0,
    -- NULL counts as due, which is what makes a freshly inserted row claimable
    -- by the next worker pass without a second write.
    next_attempt_at  TIMESTAMPTZ,
    -- When the verdict was decided. It is what expires a verdict: a row older
    -- than the shared VerdictTTL is re-scanned rather than trusted, because
    -- reputation changes in both directions.
    decided_at       TIMESTAMPTZ,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT link_scans_status_check CHECK (status IN ('pending', 'safe', 'malicious')),
    CONSTRAINT link_scans_url_length_check CHECK (char_length(canonical_url) <= 2048),
    -- A decided row must say when, and a pending row must not pretend to have.
    CONSTRAINT link_scans_decided_check CHECK (
        (status = 'pending' AND decided_at IS NULL)
        OR (status <> 'pending' AND decided_at IS NOT NULL)
    )
);

-- The worker's claim predicate, and nothing else. Partial so the index is empty
-- whenever there is no backlog, which is what makes idle polling almost free —
-- the same property RF-22's scan queue relies on.
CREATE INDEX idx_link_scans_due
    ON chat.link_scans (next_attempt_at NULLS FIRST, created_at)
    WHERE status = 'pending';

-- Expiry sweeps and verdict reads by freshness.
CREATE INDEX idx_link_scans_decided_at
    ON chat.link_scans (decided_at)
    WHERE status <> 'pending';

-- ---------------------------------------------------------------------------
-- message_link_scans: which URLs a withheld message is waiting on
-- ---------------------------------------------------------------------------
--
-- Without this the worker would have to re-extract URLs from message bodies to
-- find out who was waiting on a verdict, which means re-running the parser over
-- every pending body on every pass and trusting it to produce the same answer it
-- produced at write time. The edge is recorded once, in the same statement that
-- creates the message, so "message created but its links not recorded" is
-- unreachable — the same argument message_attachments makes in 000021.
--
-- ON DELETE CASCADE on the message side only. link_scans rows outlive the
-- messages that caused them, because the verdict is about the URL and the next
-- message naming it should not have to pay for a new scan.
CREATE TABLE chat.message_link_scans (
    message_id    UUID NOT NULL REFERENCES chat.messages (id) ON DELETE CASCADE,
    canonical_url TEXT NOT NULL REFERENCES chat.link_scans (canonical_url) ON DELETE CASCADE,

    PRIMARY KEY (message_id, canonical_url)
);

-- "Which messages are waiting on this URL", the direction the worker reads
-- after a verdict lands. The primary key already serves the other direction.
CREATE INDEX idx_message_link_scans_url
    ON chat.message_link_scans (canonical_url);

-- The pending backlog itself, for the pass that promotes or blocks messages
-- once their URLs are decided. Partial for the same reason as above: it holds
-- nothing at all in the steady state where every message is published.
CREATE INDEX idx_messages_pending_link_scan
    ON chat.messages (created_at)
    WHERE status = 'pending_link_scan';

COMMIT;
