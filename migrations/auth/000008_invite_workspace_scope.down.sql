-- 000008_invite_workspace_scope.down.sql
-- Reverts the workspace binding on auth.user_invites (issue #425).
--
-- The invites revoked by the up migration are NOT restored: they were revoked
-- because their workspace could not be determined, and rolling back does not
-- make that information available. Re-issuing them from the admin UI is the
-- supported path.
--
-- migrations/chat/000019 drops the foreign key and must be rolled back first;
-- the runner applies down migrations in reverse order, so it already is.

BEGIN;

SET LOCAL search_path = auth, public;

DROP INDEX IF EXISTS auth.idx_user_invites_inviter_window;
DROP INDEX IF EXISTS auth.idx_user_invites_workspace_status;
DROP INDEX IF EXISTS auth.idx_user_invites_pending_workspace_email;

ALTER TABLE auth.user_invites
    DROP CONSTRAINT IF EXISTS user_invites_pending_workspace_check;

ALTER TABLE auth.user_invites
    DROP COLUMN IF EXISTS workspace_id;

COMMIT;
