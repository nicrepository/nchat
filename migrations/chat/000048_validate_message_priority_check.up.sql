BEGIN;

-- The scanning half of 000047's CHECK constraint (issue #821).
--
-- 000047 adds it NOT VALID, which is a catalogue write: it enforces every new
-- row immediately but does not read the existing ones. This pass performs that
-- read and marks the constraint validated, so the planner may rely on it.
--
-- Lock taken here: SHARE UPDATE EXCLUSIVE. It conflicts with DDL and with
-- VACUUM FULL, and with nothing an application does — sending, editing,
-- deleting and reading messages all proceed while it runs.
--
-- The scan cannot fail: the column is NOT NULL DEFAULT 'standard', so every row
-- that predates 000047 reads as 'standard', and every row written since has
-- passed this very constraint.
ALTER TABLE chat.messages VALIDATE CONSTRAINT messages_priority_check;

COMMIT;
