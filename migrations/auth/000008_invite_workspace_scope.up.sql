-- 000008_invite_workspace_scope.up.sql
-- Binds invites to the workspace that issued them, and marks which invite
-- opens a workspace's first administrative membership.
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

-- Nullable, and it stays nullable. Rows written before this migration name no
-- workspace and none can be inferred — the column never existed, and no other
-- table records which workspace an invite was meant for. Rather than guess or
-- destroy, this migration leaves every existing row byte-for-byte as it was:
-- same status, same timestamps, workspace_id NULL. Such a row is a legacy
-- unscoped invite, and the acceptance path refuses it outright
-- (domain.ErrInviteWorkspaceMissing) because there is no membership it could
-- create. Leaving the data intact is what makes this migration reversible: the
-- down below restores the previous schema exactly, with no state to reconstruct.
ALTER TABLE auth.user_invites
    ADD COLUMN workspace_id UUID;

-- Which membership the invite confers, decided server-side at issuance and
-- never expressible in a request. 'member' is the default so every pre-existing
-- row keeps the only behaviour it ever had. 'bootstrap_owner' is reachable only
-- through the bootstrap credential and creates the workspace's first owner,
-- which is what closes the bootstrap window on first acceptance.
ALTER TABLE auth.user_invites
    ADD COLUMN invite_kind TEXT NOT NULL DEFAULT 'member';

ALTER TABLE auth.user_invites
    ADD CONSTRAINT user_invites_invite_kind_check
        CHECK (invite_kind IN ('member', 'bootstrap_owner'));

-- NOT VALID on purpose, and it is the whole reversibility argument: the
-- invariant binds every row this application writes from now on, while the
-- legacy pending rows are neither validated nor rewritten. PostgreSQL still
-- enforces it on INSERT and UPDATE, so a new invite cannot be created without a
-- workspace.
ALTER TABLE auth.user_invites
    ADD CONSTRAINT user_invites_pending_workspace_check
        CHECK (status <> 'pending' OR workspace_id IS NOT NULL)
        NOT VALID;

-- At most one pending invite per (workspace, email). Scoping the uniqueness to
-- the workspace is the point: a global constraint here would reintroduce the
-- cross-tenant block this migration removes. Partial on 'pending' so accepted,
-- expired and revoked invites accumulate as history without colliding, and
-- explicitly partial on a non-null workspace so legacy unscoped rows are
-- outside the index rather than relying on NULL's behaviour under UNIQUE.
CREATE UNIQUE INDEX idx_user_invites_pending_workspace_email
    ON auth.user_invites (workspace_id, email)
    WHERE workspace_id IS NOT NULL
      AND status = 'pending';

-- Serves the per-workspace invite listing and the tenant-scoped lookups.
CREATE INDEX idx_user_invites_workspace_status
    ON auth.user_invites (workspace_id, status);

-- Serves the rate-limit count, which is keyed by (actor, workspace) over a
-- recent time window.
CREATE INDEX idx_user_invites_inviter_window
    ON auth.user_invites (invited_by_user_id, workspace_id, created_at);

-- Serves the bootstrap lifecycle: locating a workspace's outstanding
-- bootstrap invites to revoke them once the first owner exists.
CREATE INDEX idx_user_invites_workspace_kind_status
    ON auth.user_invites (workspace_id, invite_kind, status);

COMMIT;
