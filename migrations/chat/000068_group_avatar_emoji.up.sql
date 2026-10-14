-- Issue #1026: a group's own identity — Automático or one emoji.
--
-- NULL is Automático: the initials are derived from the current name on every
-- render, so a rename recomputes them and nothing derived is stored. A value is
-- one catalogued Unicode emoji sequence, validated by chat-service against the
-- embedded catalog (#496); the CHECK below is the structural backstop only —
-- groups alone, and bounded far above the longest catalogued sequence (10 code
-- points) so no payload can grow the row. No colour, initials or markup column.
--
-- Blue/Green: additive only. A nullable column without a default is invisible
-- to a build that predates it, and every existing group reads as Automático.
BEGIN;

ALTER TABLE chat.dm_conversations
    ADD COLUMN avatar_emoji TEXT NULL;

ALTER TABLE chat.dm_conversations
    ADD CONSTRAINT dm_conversations_avatar_emoji_check
    CHECK (avatar_emoji IS NULL OR (type = 'group' AND char_length(avatar_emoji) BETWEEN 1 AND 16));

COMMIT;
