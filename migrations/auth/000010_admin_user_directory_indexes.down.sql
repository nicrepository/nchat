-- 000010_admin_user_directory_indexes.down.sql
-- Drops the indexes added for the Admin Console user directory (issue #579).
--
-- Index-only, so the rollback loses no data and changes no constraint: the
-- queries that used them still return the same rows, more slowly.

BEGIN;

SET LOCAL search_path = auth, public;

DROP INDEX IF EXISTS auth.idx_admin_principal_roles_role;
DROP INDEX IF EXISTS auth.idx_users_directory_page;

COMMIT;
