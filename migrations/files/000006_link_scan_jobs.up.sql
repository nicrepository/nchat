-- 000006_link_scan_jobs.up.sql
-- RF-21 (issue #135): file-service's own durable queue for Cloudflare URL
-- Scanner verdicts.
--
-- Why file-service needs one at all:
--
--   The link preview used to submit a scan, receive the provider's uuid, answer
--   "pending", and then throw the uuid away. Nothing polled it, nothing survived
--   a restart, and every retry of the same preview submitted the URL again. The
--   feature could therefore never reach a verdict, and a client refreshing a
--   preview was a quota drain.
--
-- Why it is not the chat-service table:
--
--   chat.link_scans belongs to chat-service, which owns migrations/chat. A
--   cross-schema write would make file-service a writer of another service's
--   table and put a chat concern in a files migration — the convention
--   files/000001 states, and the same argument chat/000021 makes about
--   attachments. The two services share the *decision* (canonicalisation,
--   provider contract, verdict rules) through libs/go/platform/urlsafety; they
--   do not share storage.
--
-- The state machine has three states and two terminal transitions:
--
--   submit_pending --(scan submitted)--> polling
--   polling        --(safe)-----------> done, verdict = 'safe'
--   polling        --(malicious)------> done, verdict = 'malicious'
--
-- Nothing else is terminal. A provider that times out, answers 404 (still
-- running), or returns something unusable leaves the row exactly where it was
-- with a later next_attempt_at, because a URL nobody could scan is a URL that is
-- still not cleared.

BEGIN;

CREATE TABLE files.link_scans (
    -- SHA-256 of the canonical URL. The key is the digest and not the URL
    -- itself so the primary-key index carries no readable path or query: a
    -- canonical URL includes both, which is exactly where internal identifiers
    -- and resource names live.
    url_digest         BYTEA       PRIMARY KEY,
    -- The URL is still stored, because the worker has to submit it to the
    -- provider after a restart and a digest cannot be reversed. It is treated as
    -- potentially sensitive everywhere else: never logged, never a metric label.
    canonical_url      TEXT        NOT NULL,

    state              TEXT        NOT NULL DEFAULT 'submit_pending',
    -- The provider's scan id, set when the submission succeeds. It is what
    -- polling reads, and it is what a verdict write is compared against so a
    -- worker whose lease expired cannot overwrite a newer scan's result.
    scan_uuid          TEXT,

    -- Set only in the 'done' state.
    verdict            TEXT,
    verdict_expires_at TIMESTAMPTZ,

    attempts           SMALLINT    NOT NULL DEFAULT 0,
    -- NULL counts as due, which makes a freshly inserted row claimable on the
    -- next worker pass without a second write.
    next_attempt_at    TIMESTAMPTZ,
    lease_until        TIMESTAMPTZ,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT link_scans_state_check
        CHECK (state IN ('submit_pending', 'polling', 'done')),
    CONSTRAINT link_scans_url_length_check
        CHECK (char_length(canonical_url) <= 2048),
    CONSTRAINT link_scans_digest_length_check
        CHECK (octet_length(url_digest) = 32),
    -- A finished row must carry a verdict and an expiry; an unfinished one must
    -- not pretend to. This is what stops a half-written row from later reading
    -- as a clearance.
    CONSTRAINT link_scans_verdict_check CHECK (
        (state <> 'done' AND verdict IS NULL AND verdict_expires_at IS NULL)
        OR (state = 'done' AND verdict IN ('safe', 'malicious') AND verdict_expires_at IS NOT NULL)
    ),
    -- Polling requires something to poll.
    CONSTRAINT link_scans_polling_check CHECK (state <> 'polling' OR scan_uuid IS NOT NULL)
);

-- The worker's claim predicate, and nothing else. Partial so the index is empty
-- whenever every URL has been decided, which is what makes idle polling almost
-- free — the same property RF-22's scan queue relies on.
CREATE INDEX idx_files_link_scans_due
    ON files.link_scans (next_attempt_at NULLS FIRST, created_at)
    WHERE state <> 'done';

-- Expiry sweeps: a decided row whose verdict has lapsed must be re-scanned
-- rather than trusted, because reputation changes in both directions.
CREATE INDEX idx_files_link_scans_expiry
    ON files.link_scans (verdict_expires_at)
    WHERE state = 'done';

COMMIT;
