-- 000004_object_cleanup_jobs.up.sql
-- RF-31 (issue #464), SR-002: durable cleanup of stored objects that must not
-- be kept.
--
-- The problem this table exists for:
--
--   A preview is uploaded to SeaweedFS *before* the row that points at it can
--   be written — the wrapped key authenticates the object, so the row cannot be
--   completed until the object is durable. When the publication is then refused
--   — the scan condemned the attachment, it was removed, or a newer claim took
--   over — the object has to be deleted. If that delete fails, the only record
--   of the key was a log line and a counter, and the object stayed in storage
--   forever with nothing able to find it again.
--
--   A log is not a work queue. The key has to survive the failure in the same
--   database everything else survives in.
--
-- Design decisions:
--   - object_key is UNIQUE, which is what makes enqueue idempotent: the same
--     failed delete can be recorded any number of times and produces one job.
--     It is also the only identifier here — no attachment id, no workspace, no
--     preview id — because a job is about an object nothing points at any more,
--     and carrying a link to a row that may be deleted would create a reference
--     that outlives its referent.
--   - attempts and next_attempt_at are the same scheduler the preview job uses
--     (migration 000003): claim leases the row by pushing the next attempt out
--     and counting the try. No 'processing' state, so a crashed worker leaves
--     nothing to reconcile.
--   - There is no status column. A finished job is deleted: the queue is the
--     backlog, and a completed cleanup is not information anyone needs to keep.
--   - Nothing sensitive is stored. The key is a server-generated path
--     (nchat/previews/<uuid>) with no user input in it, and there is no room
--     here for content, plaintext, key material, tokens or driver text.

BEGIN;

CREATE TABLE files.object_cleanup_jobs (
    id              UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    object_key      TEXT        NOT NULL,
    attempts        SMALLINT    NOT NULL DEFAULT 0,
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- One job per object: the enqueue is an upsert that does nothing on
    -- conflict, so a delete that fails repeatedly cannot grow the queue.
    CONSTRAINT object_cleanup_jobs_object_key_unique UNIQUE (object_key),
    CONSTRAINT object_cleanup_jobs_attempts_check CHECK (attempts >= 0),
    -- The same shape the storage client accepts, so a key this table can hold
    -- is a key that client can address.
    CONSTRAINT object_cleanup_jobs_object_key_length_check CHECK (
        char_length(object_key) BETWEEN 1 AND 128
    )
);

-- The worker's queue: due rows, oldest first. Partial on nothing, because every
-- row in this table is outstanding work by definition — a finished job is gone.
CREATE INDEX idx_object_cleanup_jobs_due
    ON files.object_cleanup_jobs (next_attempt_at);

COMMIT;
