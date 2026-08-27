-- 000041_validate_message_reactions_emoji_check.up.sql
-- RF-03 (issue #496): the scanning half of 000040's CHECK constraint.
--
-- 000040 replaced message_reactions_emoji_check with a wider bound and left it
-- NOT VALID, which is a catalogue write: it enforces every new row immediately
-- but does not read the existing ones. This migration performs that read and
-- marks the constraint validated.
--
-- Lock taken here: SHARE UPDATE EXCLUSIVE. It conflicts with DDL and with
-- VACUUM FULL, and with nothing an application does — reacting, un-reacting and
-- reading reactions all proceed while it runs.
--
-- The scan cannot fail: the bound it checks is strictly wider than the one every
-- stored row was written under. This is bookkeeping that makes the catalogue
-- agree with reality, so the planner may rely on the constraint.

BEGIN;

ALTER TABLE chat.message_reactions
    VALIDATE CONSTRAINT message_reactions_emoji_check;

COMMIT;
