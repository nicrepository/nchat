-- 000003_chat_dm_conversations.down.sql
BEGIN;

DROP INDEX IF EXISTS chat.idx_dm_members_conversation_active;
DROP INDEX IF EXISTS chat.idx_dm_members_user;
DROP TABLE IF EXISTS chat.dm_members;

DROP INDEX IF EXISTS chat.idx_dm_conversations_direct_pair_unique;
DROP INDEX IF EXISTS chat.idx_dm_conversations_active;
DROP INDEX IF EXISTS chat.idx_dm_conversations_workspace;
DROP TABLE IF EXISTS chat.dm_conversations;

COMMIT;
