BEGIN;

-- The scanning half of 000050's three CHECK constraints (issue #825).
--
-- 000050 adds each NOT VALID, which is a catalogue write: every new row is
-- enforced immediately, the existing ones are not read. This pass performs that
-- read and marks them validated, so the planner may rely on them.
--
-- Lock taken here: SHARE UPDATE EXCLUSIVE. It conflicts with DDL and with
-- VACUUM FULL, and with nothing an application does — sending, acknowledging,
-- delivering and reading all proceed while it runs. The same two-step
-- 000047/000048 and 000042/000043 use, for the same reason: both
-- chat.messages and chat.notification_outbox grow with every message ever sent,
-- so validating inline would hold ACCESS EXCLUSIVE over the whole scan.
--
-- None of the three scans can fail. persistent_notifications is NOT NULL
-- DEFAULT false, so every row that predates 000050 satisfies the priority rule
-- vacuously; reminder_count is NOT NULL DEFAULT 0; and the widened kind vocabulary
-- is a strict superset of the one 000043 already validated.
ALTER TABLE chat.messages
    VALIDATE CONSTRAINT messages_persistent_notifications_priority_check;

ALTER TABLE chat.message_acknowledgements
    VALIDATE CONSTRAINT message_acknowledgements_reminder_count_check;

ALTER TABLE chat.notification_outbox
    VALIDATE CONSTRAINT notification_outbox_kind_check;

COMMIT;
