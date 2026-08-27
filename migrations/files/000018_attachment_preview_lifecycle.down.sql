BEGIN;

DROP INDEX IF EXISTS files.idx_attachments_preview_available_expiry;
DROP INDEX IF EXISTS files.idx_attachments_preview_generating_lease;
DROP INDEX IF EXISTS files.idx_attachments_preview_lifecycle_pending;
DROP TRIGGER IF EXISTS attachments_preview_lifecycle_sync ON files.attachments;
DROP FUNCTION IF EXISTS files.sync_attachment_preview_lifecycle();

ALTER TABLE files.attachments
    DROP CONSTRAINT IF EXISTS attachments_preview_expiry_check,
    DROP CONSTRAINT IF EXISTS attachments_preview_failure_reason_check,
    DROP CONSTRAINT IF EXISTS attachments_preview_lifecycle_status_check,
    DROP COLUMN IF EXISTS preview_expires_at,
    DROP COLUMN IF EXISTS preview_failure_reason,
    DROP COLUMN IF EXISTS preview_lifecycle_status;

COMMIT;
