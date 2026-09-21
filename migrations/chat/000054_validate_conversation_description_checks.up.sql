BEGIN;

-- The scanning half of 000053's CHECK constraints (issue #894).
--
-- 000053 adds them NOT VALID, which is a catalogue write: every new row is
-- enforced immediately but the existing ones are not read. This pass performs
-- that read and marks both constraints validated, so the planner may rely on
-- them.
--
-- Lock taken here: SHARE UPDATE EXCLUSIVE. It conflicts with DDL and with
-- VACUUM FULL, and with nothing an application does — creating channels,
-- opening conversations, sending and reading messages all proceed while it
-- runs.
--
-- The scans cannot fail: the columns were added nullable with no default in
-- 000053, so every row that predates it reads NULL, and every row written since
-- has passed these very constraints.
ALTER TABLE chat.channels VALIDATE CONSTRAINT channels_description_length_check;
ALTER TABLE chat.dm_conversations VALIDATE CONSTRAINT dm_conversations_description_length_check;

COMMIT;
