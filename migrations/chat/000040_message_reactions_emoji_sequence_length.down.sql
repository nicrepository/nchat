-- 000040_message_reactions_emoji_sequence_length.down.sql
-- Restores 000008's 1..8 code point bound on chat.message_reactions.emoji.
--
-- This rollback narrows the constraint, so unlike the forward migration it can
-- fail: any reaction stored with a longer sequence while the wider bound was in
-- force still exists, and the validating ADD below will refuse. That refusal is
-- the correct outcome — silently deleting people's reactions to make a rollback
-- succeed would be worse — and the operator's remedy is to remove those rows
-- deliberately before rolling back.
--
-- Both statements share one transaction, so the column is never unconstrained.

BEGIN;

ALTER TABLE chat.message_reactions
    DROP CONSTRAINT message_reactions_emoji_check;

ALTER TABLE chat.message_reactions
    ADD CONSTRAINT message_reactions_emoji_check
    CHECK (char_length(emoji) BETWEEN 1 AND 8);

COMMIT;
