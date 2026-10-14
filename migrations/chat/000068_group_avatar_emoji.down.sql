BEGIN;
ALTER TABLE chat.dm_conversations DROP CONSTRAINT IF EXISTS dm_conversations_avatar_emoji_check;
ALTER TABLE chat.dm_conversations DROP COLUMN IF EXISTS avatar_emoji;
COMMIT;
