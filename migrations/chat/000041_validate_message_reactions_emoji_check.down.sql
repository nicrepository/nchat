-- 000041_validate_message_reactions_emoji_check.down.sql
-- Returns message_reactions_emoji_check to the NOT VALID state 000040 leaves it
-- in.
--
-- PostgreSQL has no "invalidate constraint", so the constraint is dropped and
-- re-added NOT VALID. Both statements are in one transaction, so there is no
-- instant in which the column is unconstrained: a concurrent writer either sees
-- the old constraint or the new one, never neither.
--
-- The re-added constraint is byte-identical to 000040's, so re-running 000041
-- afterwards validates it again.

BEGIN;

ALTER TABLE chat.message_reactions
    DROP CONSTRAINT message_reactions_emoji_check;

ALTER TABLE chat.message_reactions
    ADD CONSTRAINT message_reactions_emoji_check
    CHECK (char_length(emoji) BETWEEN 1 AND 32)
    NOT VALID;

COMMIT;
