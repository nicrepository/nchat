-- 000043_validate_notification_outbox_event_contract.down.sql
-- Returns 000042's constraints to the NOT VALID state it leaves them in.
--
-- PostgreSQL has no "invalidate constraint", so each one is dropped and re-added
-- NOT VALID. Every statement is in one transaction, so there is no instant in
-- which the table is unconstrained: a concurrent writer sees the old constraint
-- or the new one, never neither.
--
-- Each re-added constraint is byte-identical to 000042's, so re-running 000043
-- afterwards validates it again.

BEGIN;

ALTER TABLE chat.notification_outbox
    DROP CONSTRAINT notification_outbox_kind_check;

ALTER TABLE chat.notification_outbox
    ADD CONSTRAINT notification_outbox_kind_check
    CHECK (kind IN (
        'direct_message', 'mention', 'reply', 'channel_message', 'reaction', 'call'
    )) NOT VALID;

ALTER TABLE chat.notification_outbox
    DROP CONSTRAINT notification_outbox_status_check;

ALTER TABLE chat.notification_outbox
    ADD CONSTRAINT notification_outbox_status_check
    CHECK (status IN (
        'pending', 'eligible', 'suppressed', 'processing', 'sent', 'retrying', 'failed'
    )) NOT VALID;

ALTER TABLE chat.notification_outbox
    DROP CONSTRAINT notification_outbox_source_type_check,
    DROP CONSTRAINT notification_outbox_priority_check,
    DROP CONSTRAINT notification_outbox_origin_check,
    DROP CONSTRAINT notification_outbox_suppressed_reason_check,
    DROP CONSTRAINT notification_outbox_dedupe_key_check;

ALTER TABLE chat.notification_outbox
    ADD CONSTRAINT notification_outbox_source_type_check
        CHECK (source_type IN ('message', 'reaction', 'call')) NOT VALID,
    ADD CONSTRAINT notification_outbox_priority_check
        CHECK (priority IN ('high', 'normal', 'low')) NOT VALID,
    ADD CONSTRAINT notification_outbox_origin_check
        CHECK (origin IN ('live', 'import', 'replay', 'resync')) NOT VALID,
    ADD CONSTRAINT notification_outbox_suppressed_reason_check
        CHECK (
            (status = 'suppressed') = (suppressed_reason IS NOT NULL)
            AND (suppressed_reason IS NULL OR char_length(suppressed_reason) BETWEEN 1 AND 200)
        ) NOT VALID,
    ADD CONSTRAINT notification_outbox_dedupe_key_check
        CHECK (dedupe_key IS NULL OR char_length(dedupe_key) BETWEEN 1 AND 200) NOT VALID;

COMMIT;
