-- 000033_admin_directory_pagination_indexes.down.sql
-- Drops the indexes added for the Admin Console's conversation and policy
-- directories (issue #579).
--
-- Index-only, so the rollback loses no data and changes no constraint: both
-- listings still return the same rows, by sorting for them.

BEGIN;

SET LOCAL search_path = chat, public;

DROP INDEX IF EXISTS chat.idx_workspaces_directory_page;
DROP INDEX IF EXISTS chat.idx_dm_conversations_directory_page;

COMMIT;
