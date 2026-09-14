BEGIN;

-- Persistent notifications for urgent messages (issue #825, parent #820).
--
-- Three facts, and the shape of this migration is the argument for each.
--
-- 1. chat.messages.persistent_notifications: the author asked for the message
--    to keep asking. Like priority (000047) and acknowledgement_required
--    (000049) it is the author's own claim about their own message, it
--    authorises nothing, and no predicate anywhere reads it to decide access.
--
-- 2. chat.message_acknowledgements gains the reminder schedule. It is not a new
--    table, deliberately: the per-recipient state machine #820 specifies —
--    pending / acknowledged / responded / expired / cancelled, only pending
--    eligible for a reminder — is already this table's, and 000049 declared the
--    `expired` value for exactly this worker. A second table would be a second
--    answer to "is this recipient still waiting", and every transition #824
--    already writes would have to be mirrored into it.
--
-- 3. chat.notification_outbox learns the `urgent_reminder` event type, so a
--    reminder is a notification like any other: same worker, same claim, same
--    lease, same retry, same policy, same dedupe index. No second scheduler and
--    no second queue.
--
-- nchat:blue-green contract-phase the only removal is the legacy UNIQUE
-- 000006 created, which 000042 kept alive through its expand window and
-- documented as removable "in a later contract release". Nothing in the current
-- release names it: both producers write `ON CONFLICT DO NOTHING` with no
-- arbiter, and the broader notification_outbox_dedupe_uq covers every row,
-- because both producers write dedupe_key and 000042 backfilled the rest.
-- Everything else here is strictly additive.

-- ---------------------------------------------------------------------------
-- The author's intent
-- ---------------------------------------------------------------------------
--
-- NOT NULL DEFAULT false for the same reason 000049's flag is: "no answer" is
-- not a state this product has, and every message written before this column
-- existed asked for no reminders. No table rewrite — PostgreSQL 11+ records an
-- ADD COLUMN ... DEFAULT in the catalogue.
ALTER TABLE chat.messages
    ADD COLUMN persistent_notifications BOOLEAN NOT NULL DEFAULT false;

-- Persistent notifications exist only for urgent messages (#820: "disponível
-- apenas para mensagens Urgent"). The service refuses the combination before it
-- reaches here; this is what makes it unreachable through any other path — an
-- importer, a repair script, a psql session — rather than only through the
-- endpoint guarded today.
--
-- NOT VALID: every existing row carries false and therefore already satisfies
-- it, and validating here would scan a table that grows with every message ever
-- sent under ACCESS EXCLUSIVE. 000051 performs that scan under SHARE UPDATE
-- EXCLUSIVE, the same two-step 000047/000048 uses.
ALTER TABLE chat.messages
    ADD CONSTRAINT messages_persistent_notifications_priority_check
    CHECK (NOT persistent_notifications OR priority = 'urgent')
    NOT VALID;

-- ---------------------------------------------------------------------------
-- The reminder schedule, per recipient
-- ---------------------------------------------------------------------------
--
-- next_reminder_at is the whole queue. It is nullable and NULL is the resting
-- state — every row that predates this migration has it, and so does every
-- recipient of a message that never asked for reminders — so "which reminders
-- are due" is answered by an index over the few rows that carry a value rather
-- than by a scan over every acknowledgement ever recorded.
--
-- It is written by the server and only by the server: the interval is a
-- constant of libs/go/platform/notificationevent, never a request field, so
-- there is no payload in which a client names when it wants to be reminded or
-- how often.
--
-- reminder_count is what bounds the loop. #825 requires reminders to be finite,
-- and a count is the finite bound that needs no second timestamp: the ceiling
-- is notificationevent.MaxUrgentReminders, and reaching it is what produces the
-- `expired` state 000049 declared and left without a producer.
ALTER TABLE chat.message_acknowledgements
    ADD COLUMN next_reminder_at TIMESTAMPTZ,
    ADD COLUMN reminder_count   SMALLINT NOT NULL DEFAULT 0;

ALTER TABLE chat.message_acknowledgements
    ADD CONSTRAINT message_acknowledgements_reminder_count_check
    CHECK (reminder_count >= 0)
    NOT VALID;

-- The due-reminder queue.
--
-- Partial over exactly the predicate the scheduler runs, so its size is the
-- number of live reminders rather than the number of acknowledgements: a
-- recipient who answered, a request that expired and every row of every message
-- that never asked for reminders are all outside it. Without this, finding due
-- reminders every few seconds would be a sequential scan over a table that only
-- grows.
--
-- Ordered by next_reminder_at alone: the scheduler takes the oldest due rows
-- first and the index supplies that order, so the LIMIT stops the scan after a
-- batch.
CREATE INDEX idx_message_acknowledgements_due
    ON chat.message_acknowledgements (next_reminder_at)
    WHERE state = 'pending' AND next_reminder_at IS NOT NULL;

-- ---------------------------------------------------------------------------
-- A reminder is a notification
-- ---------------------------------------------------------------------------
--
-- Widening a CHECK, exactly as 000042 widened this same one: every value the
-- running release writes still passes, and every value it reads still exists.
--
-- `urgent_reminder` is its own event type rather than a repeat of the message's
-- original one, because the two are different facts and three consumers need to
-- tell them apart: the metric that counts reminders, the operator reading a
-- suppression, and the browser choosing a title. A repeat carrying the original
-- kind would also be indistinguishable in the outbox itself.
ALTER TABLE chat.notification_outbox
    DROP CONSTRAINT notification_outbox_kind_check;

ALTER TABLE chat.notification_outbox
    ADD CONSTRAINT notification_outbox_kind_check
    CHECK (kind IN (
        'direct_message', 'mention', 'reply', 'channel_message', 'reaction',
        'call', 'urgent_reminder'
    )) NOT VALID;

-- The expand window 000042 opened closes here.
--
-- UNIQUE (message_id, recipient_user_id, kind) expresses "one notification of
-- this kind per recipient per message", which is exactly what a reminder must
-- not be: the fifth reminder for a message is a fifth row, distinguished from
-- the fourth by its occurrence number in dedupe_key. Keeping the constraint
-- would silently cap every message at one reminder, with the ON CONFLICT
-- absorbing the rest as if they had already been delivered.
--
-- Uniqueness is not lost. notification_outbox_dedupe_uq over
-- (workspace_id, recipient_user_id, dedupe_key) is strictly finer — it qualifies
-- by tenant, which this one never did — and covers every row in the table: both
-- producers write dedupe_key and 000042 backfilled the rows that predate it.
ALTER TABLE chat.notification_outbox
    DROP CONSTRAINT notification_outbox_message_recipient_unique;

COMMIT;
