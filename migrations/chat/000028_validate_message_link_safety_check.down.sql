-- 000028_validate_message_link_safety_check.down.sql
-- Returns messages_link_safety_state_check to the NOT VALID state 000027 leaves
-- it in.
--
-- PostgreSQL has no "invalidate constraint", so the constraint is dropped and
-- re-added NOT VALID. Both statements are in one transaction, so there is no
-- instant in which chat.messages is unconstrained: a concurrent writer either
-- sees the old constraint or the new one, never neither.
--
-- The re-added constraint is byte-identical to 000027's, so re-running 000028
-- afterwards validates it again.

BEGIN;

ALTER TABLE chat.messages
    DROP CONSTRAINT messages_link_safety_state_check;

ALTER TABLE chat.messages
    ADD CONSTRAINT messages_link_safety_state_check
    CHECK (link_safety_state IN ('', 'safe', 'inconclusive', 'malicious'))
    NOT VALID;

COMMIT;
