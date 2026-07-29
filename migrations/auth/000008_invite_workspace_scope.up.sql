-- 000008_invite_workspace_scope.up.sql
-- Binds admin invites to the workspace that issued them (issue #425).
--
-- Until now auth.user_invites had no workspace column, so an invite belonged to
-- nobody in particular. Three consequences, all of them cross-tenant:
--
--   * accepting an invite created an account with no membership, leaving the
--     invited person unable to reach any workspace;
--   * the "already pending" check was global, so an admin of workspace A could
--     block workspace B from inviting the same address;
--   * nothing recorded which admin issued an invite, so per-actor abuse could
--     be neither attributed nor limited.
--
-- The foreign key to chat.workspaces is added by migrations/chat/000019, not
-- here: the runner applies every auth migration before any chat migration, so
-- chat.workspaces does not exist yet at this point.

BEGIN;

SET LOCAL search_path = auth, public;

ALTER TABLE auth.user_invites
    ADD COLUMN workspace_id UUID;

-- Legacy rows carry no workspace and none can be inferred: the column never
-- existed, and no other table records which workspace an invite was meant for.
-- Guessing would place a real person in a workspace nobody chose, so pending
-- invites are revoked instead. This fails closed by design — a revoked invite
-- can be re-issued from the UI, an incorrect membership cannot be undone by
-- the person it affects. Accepted, expired and already-revoked rows are left
-- untouched so the audit trail survives.
UPDATE auth.user_invites
SET status     = 'revoked',
    revoked_at = COALESCE(revoked_at, now()),
    updated_at = now()
WHERE workspace_id IS NULL
  AND status = 'pending';

-- Every invite that can still be accepted must name its workspace. Historical
-- rows keep workspace_id NULL, which is why this is a conditional check rather
-- than NOT NULL: accepting is gated on status = 'pending', so an invite without
-- a workspace is unreachable.
ALTER TABLE auth.user_invites
    ADD CONSTRAINT user_invites_pending_workspace_check
        CHECK (status <> 'pending' OR workspace_id IS NOT NULL);

-- At most one pending invite per (workspace, email). Scoping the uniqueness to
-- the workspace is the point: a global constraint here would reintroduce the
-- cross-tenant block this migration removes. Partial on 'pending' so accepted,
-- expired and revoked invites accumulate as history without colliding.
CREATE UNIQUE INDEX idx_user_invites_pending_workspace_email
    ON auth.user_invites (workspace_id, email)
    WHERE status = 'pending';

-- Serves the per-workspace invite listing and the tenant-scoped lookups.
CREATE INDEX idx_user_invites_workspace_status
    ON auth.user_invites (workspace_id, status);

-- Serves the rate-limit count, which is keyed by (actor, workspace) over a
-- recent time window.
CREATE INDEX idx_user_invites_inviter_window
    ON auth.user_invites (invited_by_user_id, workspace_id, created_at);

COMMIT;
