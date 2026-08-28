-- 000001_file_attachments.up.sql
-- RF-30/RF-32/RF-33 (issue #424): metadata for channel and DM attachments
-- uploaded through file-service and persisted encrypted in SeaweedFS.
--
-- Design decisions:
--   - files schema keeps attachment metadata out of auth, chat and public.
--   - id is the public identifier handed to clients: a random UUID, never a
--     sequence, so attachment ids are not enumerable. The SeaweedFS object key
--     and the wrapped DEK stay server-side and are never serialised to a client.
--   - workspace_id / channel_id / conversation_id reference chat.* by
--     convention only. Cross-schema foreign keys are not used anywhere in this
--     repository (see 000001_chat_domain_schema.up.sql, which references
--     auth.users.id the same way), so files migrations stay independent of the
--     chat domain's migration order. Referential integrity for the destination
--     is enforced at write time: a row is only ever inserted after the same
--     query that authorises the uploader has resolved the destination and its
--     canonical workspace.
--   - destination_kind plus the exclusivity CHECK make "exactly one logical
--     destination" a database invariant instead of an application convention.
--   - status is closed by CHECK and always starts at pending_upload. The client
--     never supplies it. pending_scan is the hand-off state for the future
--     asynchronous antimalware worker (RF-33 scan, out of scope here); only
--     clean is downloadable.
--   - wrapped_dek stores the per-object data key sealed under the service KEK
--     (AES-256-GCM). The plaintext DEK is never persisted, logged or returned.
--   - failure_code is a short sanitised operational code (e.g. 'storage_write'),
--     never a driver message, so a failed upload stays diagnosable without
--     leaking storage topology into the database.

BEGIN;

CREATE SCHEMA IF NOT EXISTS files;

CREATE TABLE files.attachments (
    id                    UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id          UUID        NOT NULL,
    uploader_id           UUID        NOT NULL,
    destination_kind      TEXT        NOT NULL,
    channel_id            UUID,
    conversation_id       UUID,
    original_filename     TEXT        NOT NULL,
    declared_mime         TEXT        NOT NULL,
    detected_mime         TEXT,
    size_bytes            BIGINT      NOT NULL DEFAULT 0,
    ciphertext_size_bytes BIGINT      NOT NULL DEFAULT 0,
    storage_provider      TEXT        NOT NULL,
    storage_object_key    TEXT        NOT NULL,
    envelope_version      SMALLINT    NOT NULL,
    wrapped_dek           BYTEA       NOT NULL,
    status                TEXT        NOT NULL DEFAULT 'pending_upload',
    failure_code          TEXT,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    uploaded_at           TIMESTAMPTZ,
    deleted_at            TIMESTAMPTZ,

    CONSTRAINT attachments_storage_object_key_unique UNIQUE (storage_object_key),
    CONSTRAINT attachments_destination_kind_check CHECK (destination_kind IN ('channel', 'dm')),
    CONSTRAINT attachments_status_check CHECK (
        status IN ('pending_upload', 'pending_scan', 'clean', 'rejected', 'failed', 'deleted')
    ),
    -- Exactly one logical destination, never both and never neither.
    CONSTRAINT attachments_destination_exclusive_check CHECK (
        (destination_kind = 'channel' AND channel_id IS NOT NULL AND conversation_id IS NULL)
        OR
        (destination_kind = 'dm' AND conversation_id IS NOT NULL AND channel_id IS NULL)
    ),
    CONSTRAINT attachments_sizes_check CHECK (size_bytes >= 0 AND ciphertext_size_bytes >= 0),
    CONSTRAINT attachments_original_filename_length_check CHECK (
        char_length(original_filename) BETWEEN 1 AND 255
    ),
    CONSTRAINT attachments_declared_mime_length_check CHECK (char_length(declared_mime) <= 255),
    CONSTRAINT attachments_detected_mime_length_check CHECK (
        detected_mime IS NULL OR char_length(detected_mime) <= 255
    ),
    CONSTRAINT attachments_failure_code_length_check CHECK (
        failure_code IS NULL OR char_length(failure_code) <= 64
    ),
    CONSTRAINT attachments_storage_provider_check CHECK (storage_provider IN ('seaweedfs')),
    CONSTRAINT attachments_envelope_version_check CHECK (envelope_version > 0)
);

-- Listing a destination's live attachments, tenant scoped.
CREATE INDEX idx_attachments_channel
    ON files.attachments (workspace_id, channel_id, created_at DESC)
    WHERE destination_kind = 'channel' AND deleted_at IS NULL;

CREATE INDEX idx_attachments_conversation
    ON files.attachments (workspace_id, conversation_id, created_at DESC)
    WHERE destination_kind = 'dm' AND deleted_at IS NULL;

-- The antimalware worker's queue and the orphan sweep both read this partial
-- index: rows still awaiting a terminal state, oldest first.
CREATE INDEX idx_attachments_pending
    ON files.attachments (status, created_at)
    WHERE status IN ('pending_upload', 'pending_scan');

COMMIT;
