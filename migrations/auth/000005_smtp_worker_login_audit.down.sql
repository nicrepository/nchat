-- 000005_smtp_worker_login_audit.down.sql
-- Reverts SMTP worker columns and 'processing' status addition.

BEGIN;

SET LOCAL search_path = auth, public;

DROP INDEX IF EXISTS auth.idx_login_attempts_user_failed_created_id;
DROP INDEX IF EXISTS auth.idx_email_outbox_claimable;

UPDATE auth.email_outbox SET status = 'pending' WHERE status = 'processing';

ALTER TABLE auth.email_outbox
    DROP COLUMN IF EXISTS processing_deadline_at,
    DROP COLUMN IF EXISTS processing_started_at,
    DROP COLUMN IF EXISTS next_retry_at;

ALTER TABLE auth.email_outbox
    DROP CONSTRAINT email_outbox_status_check;

ALTER TABLE auth.email_outbox
    ADD CONSTRAINT email_outbox_status_check
        CHECK (status IN ('pending', 'sent', 'failed'));

COMMIT;
