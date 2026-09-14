BEGIN;

-- Reverts 000050, in the order that leaves a partially-applied database
-- consistent: the widest thing first, the columns last.
--
-- The legacy UNIQUE is restored before anything else, because restoring it can
-- fail — a database that has produced more than one reminder for a recipient
-- holds rows it refuses — and a failure has to abort the whole revert rather
-- than land halfway. Rolling this migration back therefore requires the
-- reminders it created to be gone, which the DROP below is what removes: the
-- outbox rows are deleted first, so the constraint is restored against exactly
-- the population it was written for.
--
-- Deleting reminder rows discards delivery history for reminders specifically.
-- That is the honest consequence of reverting the feature that produced them;
-- no other kind of notification is touched.
DELETE FROM chat.notification_outbox WHERE kind = 'urgent_reminder';

ALTER TABLE chat.notification_outbox
    ADD CONSTRAINT notification_outbox_message_recipient_unique
    UNIQUE (message_id, recipient_user_id, kind);

ALTER TABLE chat.notification_outbox
    DROP CONSTRAINT notification_outbox_kind_check;

ALTER TABLE chat.notification_outbox
    ADD CONSTRAINT notification_outbox_kind_check
    CHECK (kind IN (
        'direct_message', 'mention', 'reply', 'channel_message', 'reaction', 'call'
    )) NOT VALID;

-- ...and validated again, because that is the state 000043 left it in and a down
-- restores what it found rather than something weaker. The scan takes SHARE
-- UPDATE EXCLUSIVE — it blocks no application write — and it cannot fail: the
-- only rows the narrowed vocabulary would refuse are the reminders the DELETE
-- above has just removed.
ALTER TABLE chat.notification_outbox
    VALIDATE CONSTRAINT notification_outbox_kind_check;

DROP INDEX IF EXISTS chat.idx_message_acknowledgements_due;

ALTER TABLE chat.message_acknowledgements
    DROP CONSTRAINT IF EXISTS message_acknowledgements_reminder_count_check;

ALTER TABLE chat.message_acknowledgements
    DROP COLUMN IF EXISTS next_reminder_at,
    DROP COLUMN IF EXISTS reminder_count;

ALTER TABLE chat.messages
    DROP CONSTRAINT IF EXISTS messages_persistent_notifications_priority_check;

ALTER TABLE chat.messages
    DROP COLUMN IF EXISTS persistent_notifications;

COMMIT;
