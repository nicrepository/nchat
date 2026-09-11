BEGIN;

-- Returns messages_priority_check to the NOT VALID state 000047 leaves it in.
--
-- PostgreSQL has no "invalidate constraint", so the constraint is dropped and
-- re-added NOT VALID. Both statements are in one transaction, so there is no
-- instant in which the column is unconstrained: a concurrent writer either sees
-- the old constraint or the new one, never neither.
--
-- The re-added constraint is byte-identical to 000047's, so re-running 000048
-- afterwards validates it again.
ALTER TABLE chat.messages
    DROP CONSTRAINT messages_priority_check;

ALTER TABLE chat.messages
    ADD CONSTRAINT messages_priority_check
    CHECK (priority IN ('standard', 'important', 'urgent'))
    NOT VALID;

COMMIT;
