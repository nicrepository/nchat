BEGIN;

-- Returns both constraints to the NOT VALID state 000053 leaves them in.
--
-- PostgreSQL has no "invalidate constraint", so each is dropped and re-added
-- NOT VALID. Both pairs are in one transaction, so there is no instant in which
-- a column is unconstrained: a concurrent writer sees either the old constraint
-- or the new one, never neither.
--
-- The re-added constraints are byte-identical to 000053's, so re-running 000054
-- afterwards validates them again.
ALTER TABLE chat.channels
    DROP CONSTRAINT channels_description_length_check;

ALTER TABLE chat.channels
    ADD CONSTRAINT channels_description_length_check
    CHECK (description IS NULL OR char_length(description) <= 500)
    NOT VALID;

ALTER TABLE chat.dm_conversations
    DROP CONSTRAINT dm_conversations_description_length_check;

ALTER TABLE chat.dm_conversations
    ADD CONSTRAINT dm_conversations_description_length_check
    CHECK (description IS NULL OR char_length(description) <= 500)
    NOT VALID;

COMMIT;
