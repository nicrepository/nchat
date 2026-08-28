BEGIN;

SET LOCAL search_path = files, public;

DROP INDEX IF EXISTS files.attachments_expired_message_drafts_idx;

ALTER TABLE files.attachments
    DROP COLUMN IF EXISTS draft_expires_at;

COMMIT;
