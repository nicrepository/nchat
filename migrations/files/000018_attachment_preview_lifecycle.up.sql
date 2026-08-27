BEGIN;

ALTER TABLE files.attachments
    ADD COLUMN preview_lifecycle_status TEXT NOT NULL DEFAULT 'pending',
    ADD COLUMN preview_failure_reason TEXT,
    ADD COLUMN preview_expires_at TIMESTAMPTZ;

ALTER TABLE files.attachments
    ADD CONSTRAINT attachments_preview_lifecycle_status_check CHECK (
        preview_lifecycle_status IN ('pending', 'scanning', 'generating', 'available', 'failed', 'blocked', 'expired')
    ),
    ADD CONSTRAINT attachments_preview_failure_reason_check CHECK (
        preview_failure_reason IS NULL OR preview_failure_reason IN (
            'active_content', 'invalid_document', 'unsupported_format',
            'timeout', 'conversion_failed', 'output_too_large',
            'render_failed', 'attempts_exhausted', 'expired'
        )
    ),
    ADD CONSTRAINT attachments_preview_expiry_check CHECK (
        (preview_lifecycle_status = 'available' AND preview_expires_at IS NOT NULL)
        OR (preview_lifecycle_status <> 'available')
    );

-- Backfill before installing the synchronizer. Existing ready previews receive
-- the same fixed product TTL new writes use; the source object is never read.
UPDATE files.attachments
SET preview_lifecycle_status = CASE preview_status
        WHEN 'ready' THEN 'available'
        WHEN 'unsupported' THEN 'blocked'
        WHEN 'failed' THEN 'failed'
        ELSE 'pending'
    END,
    preview_expires_at = CASE WHEN preview_status = 'ready' THEN now() + interval '30 days' END;

CREATE FUNCTION files.sync_attachment_preview_lifecycle()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_OP = 'INSERT' OR NEW.preview_status IS DISTINCT FROM OLD.preview_status THEN
        NEW.preview_lifecycle_status := CASE NEW.preview_status
            WHEN 'ready' THEN 'available'
            WHEN 'unsupported' THEN 'blocked'
            WHEN 'failed' THEN 'failed'
            ELSE 'pending'
        END;
    ELSE
        NEW.preview_status := CASE NEW.preview_lifecycle_status
            WHEN 'available' THEN 'ready'
            WHEN 'blocked' THEN 'unsupported'
            WHEN 'failed' THEN 'failed'
            WHEN 'expired' THEN 'failed'
            ELSE 'pending'
        END;
    END IF;

    IF NEW.preview_lifecycle_status = 'available' AND NEW.preview_expires_at IS NULL THEN
        NEW.preview_expires_at := now() + interval '30 days';
    ELSIF NEW.preview_lifecycle_status <> 'available' THEN
        NEW.preview_expires_at := NULL;
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER attachments_preview_lifecycle_sync
BEFORE INSERT OR UPDATE OF preview_status, preview_lifecycle_status
ON files.attachments
FOR EACH ROW EXECUTE FUNCTION files.sync_attachment_preview_lifecycle();

CREATE INDEX idx_attachments_preview_lifecycle_pending
    ON files.attachments (preview_next_attempt_at, created_at)
    WHERE preview_lifecycle_status = 'pending' AND deleted_at IS NULL;

CREATE INDEX idx_attachments_preview_generating_lease
    ON files.attachments (preview_next_attempt_at)
    WHERE preview_lifecycle_status = 'generating' AND deleted_at IS NULL;

CREATE INDEX idx_attachments_preview_available_expiry
    ON files.attachments (preview_expires_at)
    WHERE preview_lifecycle_status = 'available' AND deleted_at IS NULL;

COMMIT;
