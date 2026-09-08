-- 000042_notification_outbox_event_contract.down.sql
-- Returns chat.notification_outbox and chat.message_pending_mentions to the
-- shape 000024 left them in.
--
-- This down is only applicable while the outbox still holds nothing the old
-- contract cannot express. Restoring the narrow CHECK constraints re-reads every
-- row, so a row whose kind is not 'mention' or whose status is one of the three
-- states 000006 never had will refuse the rollback rather than be silently
-- rewritten — which is the correct outcome: a notification whose state cannot be
-- represented must not be quietly turned into a different one.
--
-- Run it against disposable infrastructure only, as with every down in this
-- repository.

BEGIN;

DROP TRIGGER IF EXISTS notification_outbox_enforce_transition ON chat.notification_outbox;

DROP FUNCTION IF EXISTS chat.enforce_notification_outbox_transition();

ALTER TABLE chat.message_pending_mentions
    DROP COLUMN IF EXISTS priority,
    DROP COLUMN IF EXISTS kind;

DROP INDEX IF EXISTS chat.idx_notification_outbox_open;

CREATE INDEX idx_notification_outbox_pending
    ON chat.notification_outbox (created_at, id)
    WHERE status = 'pending';

DROP INDEX IF EXISTS chat.notification_outbox_dedupe_uq;

ALTER TABLE chat.notification_outbox
    DROP CONSTRAINT IF EXISTS notification_outbox_dedupe_key_check,
    DROP CONSTRAINT IF EXISTS notification_outbox_suppressed_reason_check,
    DROP CONSTRAINT IF EXISTS notification_outbox_origin_check,
    DROP CONSTRAINT IF EXISTS notification_outbox_priority_check,
    DROP CONSTRAINT IF EXISTS notification_outbox_source_type_check;

ALTER TABLE chat.notification_outbox
    DROP COLUMN IF EXISTS processed_at,
    DROP COLUMN IF EXISTS updated_at,
    DROP COLUMN IF EXISTS dedupe_key,
    DROP COLUMN IF EXISTS suppressed_reason,
    DROP COLUMN IF EXISTS origin,
    DROP COLUMN IF EXISTS priority,
    DROP COLUMN IF EXISTS occurred_at,
    DROP COLUMN IF EXISTS source_type;

ALTER TABLE chat.notification_outbox
    DROP CONSTRAINT notification_outbox_status_check;

ALTER TABLE chat.notification_outbox
    ADD CONSTRAINT notification_outbox_status_check
    CHECK (status IN ('pending', 'processing', 'sent', 'failed'));

ALTER TABLE chat.notification_outbox
    DROP CONSTRAINT notification_outbox_kind_check;

ALTER TABLE chat.notification_outbox
    ADD CONSTRAINT notification_outbox_kind_check
    CHECK (kind IN ('mention'));

COMMIT;
