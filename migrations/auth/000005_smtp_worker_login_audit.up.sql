-- 000005_smtp_worker_login_audit.up.sql
-- Adds SMTP worker columns (next_retry_at, processing_started_at, processing_deadline_at),
-- extends email_outbox status to include 'processing',
-- and indexes for claimable emails and failed login audit queries.

BEGIN;

SET LOCAL search_path = auth, public;

ALTER TABLE auth.email_outbox
    DROP CONSTRAINT email_outbox_status_check;

ALTER TABLE auth.email_outbox
    ADD CONSTRAINT email_outbox_status_check
        CHECK (status IN ('pending', 'processing', 'sent', 'failed'));

ALTER TABLE auth.email_outbox
    ADD COLUMN next_retry_at TIMESTAMPTZ,
    ADD COLUMN processing_started_at TIMESTAMPTZ,
    ADD COLUMN processing_deadline_at TIMESTAMPTZ;

CREATE INDEX idx_email_outbox_claimable ON auth.email_outbox (next_retry_at NULLS FIRST, created_at, id) WHERE status IN ('pending', 'processing');

CREATE INDEX idx_login_attempts_user_failed_created_id ON auth.login_attempts (user_id, created_at DESC, id DESC) WHERE success = false;

COMMIT;
