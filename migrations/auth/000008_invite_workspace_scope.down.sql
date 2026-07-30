-- 000008_invite_workspace_scope.down.sql
-- Reverts the workspace binding and the invite kind on auth.user_invites.
--
-- Only schema is removed; no row is touched. The up migration changed no
-- status, no timestamp and no existing value, so there is nothing to
-- reconstruct here and dropping the added objects restores the prior schema
-- exactly.
--
-- migrations/chat/000019 drops the foreign key and must be rolled back first;
-- the runner applies down migrations in reverse order, so it already is.

BEGIN;

SET LOCAL search_path = auth, public;

DROP INDEX IF EXISTS auth.idx_user_invites_workspace_kind_status;
DROP INDEX IF EXISTS auth.idx_user_invites_inviter_window;
DROP INDEX IF EXISTS auth.idx_user_invites_workspace_status;
DROP INDEX IF EXISTS auth.idx_user_invites_pending_workspace_email;

ALTER TABLE auth.user_invites
    DROP CONSTRAINT IF EXISTS user_invites_pending_workspace_check;

ALTER TABLE auth.user_invites
    DROP CONSTRAINT IF EXISTS user_invites_invite_kind_check;

ALTER TABLE auth.user_invites
    DROP COLUMN IF EXISTS invite_kind;

ALTER TABLE auth.user_invites
    DROP COLUMN IF EXISTS workspace_id;

COMMIT;
