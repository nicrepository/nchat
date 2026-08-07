-- 000003_attachment_previews.up.sql
-- RF-31 (issue #464): inline preview state for image and PDF attachments.
--
-- The preview is *derived* data: a small raster this service produces from an
-- attachment it can already decrypt. That single fact decides the whole shape
-- of this migration and is what makes it much cheaper than 000002:
--
--   - it can always be regenerated, so nothing here is irreplaceable and the
--     rollback needs no emptiness guard;
--   - it is never the file the user uploaded, so losing it degrades the UI to
--     an icon and a download button and nothing else;
--   - it must still be protected exactly like the original, because it is a
--     rendering *of* the original: same envelope, same key ring, its own data
--     key. Hence the key material columns below.
--
-- Design decisions:
--   - The job lives on the attachment row rather than in a queue table. The
--     preview belongs to exactly one attachment, is scheduled by the same
--     UPDATE that finalises that attachment's upload, and therefore cannot be
--     lost by a restart, orphaned by a failed insert, or disagree with the row
--     it describes. A second table would be a second source of truth for one
--     column's worth of state.
--   - preview_status starts at 'pending' and is written by the service, never
--     by a client. 'unsupported' is an expected absence (no renderer for this
--     content, or content outside the render limits); 'failed' is an
--     operational failure. They are separate because the client draws the same
--     fallback for both but an operator must be able to tell them apart.
--   - preview_object_id is a fresh random UUID per successful generation, and
--     it is what the preview's SeaweedFS key and its data key binding are built
--     from. Not the attachment id: giving the preview its own identity makes
--     the two objects cryptographically disjoint, so neither can be opened as
--     or substituted for the other, and it lets a retry write a brand new
--     object instead of overwriting one a concurrent attempt may be reading.
--   - preview_wrapped_dek / preview_kek_key_id / preview_envelope_version /
--     preview_dek_wrap_version mirror the attachment's own key columns. They
--     are stored rather than assumed from the running build's constants
--     because a preview written today must stay readable by a build that has
--     moved on to a new format.
--   - preview_attempts and preview_next_attempt_at are the whole scheduler:
--     claim sets the next attempt in the future (a lease) and increments the
--     count (a retry bound). There is no 'processing' state to leak on a crash
--     — an abandoned lease simply becomes due again.
--   - No column is added to any other table and no existing column changes
--     type, meaning, or nullability.
--
-- Why no lock and no emptiness guard, unlike 000002: every column added here is
-- nullable or has a DEFAULT, so an INSERT emitted by the previous build stays
-- valid. A file-service that predates this migration keeps working — it simply
-- never schedules a preview, and rows it created sit in the default 'pending'
-- until a new instance picks them up. There is no window in which a writer can
-- corrupt anything, so there is nothing to fence.

BEGIN;

ALTER TABLE files.attachments
    ADD COLUMN preview_status           TEXT        NOT NULL DEFAULT 'pending',
    ADD COLUMN preview_object_id        UUID,
    ADD COLUMN preview_size_bytes       BIGINT,
    ADD COLUMN preview_wrapped_dek      BYTEA,
    ADD COLUMN preview_kek_key_id       TEXT,
    ADD COLUMN preview_envelope_version SMALLINT,
    ADD COLUMN preview_dek_wrap_version SMALLINT,
    ADD COLUMN preview_attempts         SMALLINT    NOT NULL DEFAULT 0,
    ADD COLUMN preview_next_attempt_at  TIMESTAMPTZ;

ALTER TABLE files.attachments
    ADD CONSTRAINT attachments_preview_status_check CHECK (
        preview_status IN ('pending', 'ready', 'unsupported', 'failed')
    ),
    ADD CONSTRAINT attachments_preview_attempts_check CHECK (preview_attempts >= 0),
    ADD CONSTRAINT attachments_preview_size_check CHECK (
        preview_size_bytes IS NULL OR preview_size_bytes > 0
    ),
    ADD CONSTRAINT attachments_preview_kek_key_id_length_check CHECK (
        preview_kek_key_id IS NULL OR char_length(preview_kek_key_id) BETWEEN 1 AND 64
    ),
    -- Pinned to the versions this build implements, exactly like the
    -- attachment's own columns. A future format needs its own migration.
    ADD CONSTRAINT attachments_preview_envelope_version_check CHECK (
        preview_envelope_version IS NULL OR preview_envelope_version > 0
    ),
    ADD CONSTRAINT attachments_preview_dek_wrap_version_check CHECK (
        preview_dek_wrap_version IS NULL OR preview_dek_wrap_version = 2
    ),
    -- All of the material or none of it. A row claiming 'ready' without its
    -- object id, its length or its sealed key would be a preview that cannot be
    -- opened, which is worse than no preview at all: it would turn a fallback
    -- into an error.
    ADD CONSTRAINT attachments_preview_complete_check CHECK (
        preview_status <> 'ready'
        OR (
            preview_object_id IS NOT NULL
            AND preview_size_bytes IS NOT NULL
            AND preview_wrapped_dek IS NOT NULL
            AND preview_kek_key_id IS NOT NULL
            AND preview_envelope_version IS NOT NULL
            AND preview_dek_wrap_version IS NOT NULL
        )
    ),
    -- Two attachments can never point at the same preview object, so a delete
    -- of one preview can never strand another.
    ADD CONSTRAINT attachments_preview_object_id_unique UNIQUE (preview_object_id);

-- Historical rows: classify, do not assume.
--
-- The DEFAULT above is right for *new* rows and wrong for old ones. It gives
-- every attachment that already existed preview_status = 'pending', including
-- those that can never produce a preview: the preview claim requires a clean,
-- undeleted attachment, so a rejected, failed or removed row would sit at
-- 'pending' forever — queued for work no worker will ever pick up, and counted
-- as a backlog that does not exist.
--
-- The classification is the same one the service applies at runtime:
--
--   - deleted_at set, or status rejected/failed/deleted -> no preview is
--     possible, so the row starts at the terminal state that means exactly
--     that ('unsupported'), with nothing scheduled;
--   - pending_upload, pending_scan, clean -> the row can still legitimately
--     reach a preview once its upload finishes and the scan approves, so it
--     keeps 'pending'. A clean one is claimable immediately, which is correct:
--     it is an attachment that could have had a preview all along.
--
-- preview_next_attempt_at is NULL for every historical row by construction (the
-- column was just added), and is set here anyway so the intent is stated rather
-- than inferred from the order of the ALTERs. preview_attempts is likewise 0
-- for all of them, so there is no stale claim or lease to clear.
UPDATE files.attachments
   SET preview_status = 'unsupported',
       preview_next_attempt_at = NULL
 WHERE preview_status = 'pending'
   AND (
       deleted_at IS NOT NULL
       OR status IN ('rejected', 'failed', 'deleted')
   );

-- The worker's queue: due rows only, oldest first. The partial predicate keeps
-- the index the size of the backlog rather than the size of the table, and it
-- is the only query that reads it.
CREATE INDEX idx_attachments_preview_pending
    ON files.attachments (preview_next_attempt_at NULLS FIRST)
    WHERE preview_status = 'pending';

COMMIT;
