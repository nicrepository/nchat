-- 000032_admin_channel_directory_indexes.down.sql
-- Drops the index added for the Admin Console channel directory (issue #579).
--
-- Index-only, so the rollback loses no data and changes no constraint.

BEGIN;

SET LOCAL search_path = chat, public;

DROP INDEX IF EXISTS chat.idx_channels_directory_page;

COMMIT;
