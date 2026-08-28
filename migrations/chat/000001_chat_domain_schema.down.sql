-- 000001_chat_domain_schema.down.sql
BEGIN;

DROP TABLE IF EXISTS chat.channel_members;
DROP TABLE IF EXISTS chat.workspace_members;
DROP TABLE IF EXISTS chat.channels;
DROP TABLE IF EXISTS chat.channel_categories;
DROP TABLE IF EXISTS chat.workspaces;

COMMIT;
