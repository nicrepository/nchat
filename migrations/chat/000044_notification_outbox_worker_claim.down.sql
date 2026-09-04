-- 000044_notification_outbox_worker_claim.down.sql
-- Returns chat.notification_outbox to the shape 000042 left it in.
--
-- Dropping these columns discards the claim protocol's state: how many times a
-- notification has been attempted, when it is next due, and why it last failed.
-- Rows keep their status, so nothing becomes unrepresentable and the down is
-- always applicable — but a row sitting in 'processing' loses the only fact that
-- said its claim had been abandoned, and would need a worker running the old
-- release to be moved on by hand.
--
-- Run it against disposable infrastructure only, as with every down in this
-- repository.

BEGIN;

DROP INDEX IF EXISTS chat.idx_notification_outbox_claimable;

ALTER TABLE chat.notification_outbox
    DROP COLUMN IF EXISTS last_error,
    DROP COLUMN IF EXISTS next_attempt_at,
    DROP COLUMN IF EXISTS attempts;

COMMIT;
