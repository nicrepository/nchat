-- 000019_invite_workspace_fk.down.sql
-- Drops the invite/workspace foreign key (issue #425).
--
-- Only the constraint is removed. The workspace_id column, its check and its
-- indexes belong to migrations/auth/000008 and are rolled back there.

BEGIN;

SET LOCAL search_path = chat, auth, public;

ALTER TABLE auth.user_invites
    DROP CONSTRAINT IF EXISTS user_invites_workspace_id_fkey;

COMMIT;
