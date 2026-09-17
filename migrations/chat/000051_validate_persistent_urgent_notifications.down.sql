BEGIN;

-- Returns the three constraints to the NOT VALID state 000050 leaves them in.
--
-- PostgreSQL has no "invalidate constraint", so each is dropped and re-added
-- NOT VALID. Every pair is inside this one transaction, so there is no instant
-- in which a column is unconstrained: a concurrent writer sees the old
-- constraint or the new one, never neither.
--
-- Each re-added constraint is byte-identical to 000050's, so re-running 000051
-- afterwards validates it again.
ALTER TABLE chat.messages
    DROP CONSTRAINT messages_persistent_notifications_priority_check;

ALTER TABLE chat.messages
    ADD CONSTRAINT messages_persistent_notifications_priority_check
    CHECK (NOT persistent_notifications OR priority = 'urgent')
    NOT VALID;

ALTER TABLE chat.message_acknowledgements
    DROP CONSTRAINT message_acknowledgements_reminder_count_check;

ALTER TABLE chat.message_acknowledgements
    ADD CONSTRAINT message_acknowledgements_reminder_count_check
    CHECK (reminder_count >= 0)
    NOT VALID;

ALTER TABLE chat.notification_outbox
    DROP CONSTRAINT notification_outbox_kind_check;

ALTER TABLE chat.notification_outbox
    ADD CONSTRAINT notification_outbox_kind_check
    CHECK (kind IN (
        'direct_message', 'mention', 'reply', 'channel_message', 'reaction',
        'call', 'urgent_reminder'
    )) NOT VALID;

COMMIT;
