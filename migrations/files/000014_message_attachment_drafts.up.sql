BEGIN;

SET LOCAL search_path = files, public;

ALTER TABLE files.attachments
    ADD COLUMN draft_expires_at TIMESTAMPTZ;

CREATE INDEX attachments_expired_message_drafts_idx
    ON files.attachments (draft_expires_at, id)
    WHERE draft_expires_at IS NOT NULL AND deleted_at IS NULL;

COMMIT;
