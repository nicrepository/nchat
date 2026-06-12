-- 000004_chat_messages.down.sql
-- Reverses 000004_chat_messages.up.sql.
-- Removes only objects introduced in this migration; does not touch the schema itself.

BEGIN;

DROP INDEX IF EXISTS chat.idx_messages_referenced;
DROP INDEX IF EXISTS chat.idx_messages_forwarded;
DROP INDEX IF EXISTS chat.idx_messages_parent;
DROP INDEX IF EXISTS chat.idx_messages_sender;
DROP INDEX IF EXISTS chat.idx_messages_dm;
DROP INDEX IF EXISTS chat.idx_messages_channel;

DROP TABLE IF EXISTS chat.messages;

ALTER TABLE chat.dm_conversations
    DROP CONSTRAINT IF EXISTS dm_conversations_workspace_id_id_unique;

ALTER TABLE chat.channels
    DROP CONSTRAINT IF EXISTS channels_workspace_id_id_unique;

COMMIT;
