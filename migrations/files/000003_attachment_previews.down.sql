-- 000003_attachment_previews.down.sql
-- Reverses 000003: drops the preview state and its index.
--
-- Unlike 000002 this rollback runs against a populated table on purpose, and
-- refusing would be the wrong answer here. Nothing it drops is irreplaceable:
--
--   - every attachment, its object, its wrapped data key and its scan state are
--     untouched. Downloads keep working byte for byte;
--   - a preview is a rendering this service produced and can produce again, so
--     dropping the columns loses only cached work, never user data;
--   - the previous build simply never reads or writes these columns.
--
-- What the rollback *cannot* do is reach into SeaweedFS. Preview objects under
-- nchat/previews/ survive the DROP and become unreferenced, because the only
-- pointer to them was preview_object_id. That is a storage cleanup, not a data
-- loss, and it is deliberately not automated from a migration: a migration that
-- deletes objects would be doing destructive I/O outside the database and
-- outside its own transaction. Operators purge the prefix after rolling back —
-- see docs/api/file-attachments.md.
--
-- The order is index, then constraints, then columns: dropping the columns
-- would take the index and the constraints with them, but naming them keeps the
-- rollback explicit about everything it removes.

BEGIN;

DROP INDEX IF EXISTS files.idx_attachments_preview_pending;

ALTER TABLE files.attachments
    DROP CONSTRAINT IF EXISTS attachments_preview_complete_check,
    DROP CONSTRAINT IF EXISTS attachments_preview_object_id_unique,
    DROP CONSTRAINT IF EXISTS attachments_preview_dek_wrap_version_check,
    DROP CONSTRAINT IF EXISTS attachments_preview_envelope_version_check,
    DROP CONSTRAINT IF EXISTS attachments_preview_kek_key_id_length_check,
    DROP CONSTRAINT IF EXISTS attachments_preview_size_check,
    DROP CONSTRAINT IF EXISTS attachments_preview_attempts_check,
    DROP CONSTRAINT IF EXISTS attachments_preview_status_check;

ALTER TABLE files.attachments
    DROP COLUMN IF EXISTS preview_next_attempt_at,
    DROP COLUMN IF EXISTS preview_attempts,
    DROP COLUMN IF EXISTS preview_dek_wrap_version,
    DROP COLUMN IF EXISTS preview_envelope_version,
    DROP COLUMN IF EXISTS preview_kek_key_id,
    DROP COLUMN IF EXISTS preview_wrapped_dek,
    DROP COLUMN IF EXISTS preview_size_bytes,
    DROP COLUMN IF EXISTS preview_object_id,
    DROP COLUMN IF EXISTS preview_status;

COMMIT;
