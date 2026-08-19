-- 000030_validate_message_link_safety_check.up.sql
-- RF-21 (issue #135, CQ-005): the scanning half of 000027's CHECK constraint.
--
-- 000027 added messages_link_safety_state_check as NOT VALID, which is a
-- catalogue write: it takes ACCESS EXCLUSIVE on chat.messages but does no table
-- scan, so it returns immediately however long the history is. From that moment
-- every row written or updated is checked.
--
-- This migration performs the scan that marks the constraint validated. It is a
-- separate file rather than a second statement in 000027 because a lock is held
-- until its transaction commits: a VALIDATE next to the ADD would still have been
-- scanning under the ACCESS EXCLUSIVE the ADD took, which is exactly the outage
-- the split exists to avoid.
--
-- Lock taken here: SHARE UPDATE EXCLUSIVE. It conflicts with DDL and with VACUUM
-- FULL, and with nothing an application does — SELECT, INSERT, UPDATE and DELETE
-- on chat.messages all proceed while it runs.
--
-- The scan cannot fail: the column was created in 000027 with DEFAULT '', which
-- is one of the permitted values, and nothing between the two migrations can have
-- written anything else because the NOT VALID constraint was already enforcing
-- writes. This is bookkeeping that makes the catalogue agree with reality.

BEGIN;

ALTER TABLE chat.messages
    VALIDATE CONSTRAINT messages_link_safety_state_check;

COMMIT;
