-- 000019_invite_workspace_fk.up.sql
-- Referential integrity for the invite/workspace binding added by
-- migrations/auth/000008 (issue #425).
--
-- This lives in the chat domain purely because of apply order: the runner
-- collects migrations with `find | sort`, so every migrations/auth file runs
-- before every migrations/chat file. chat.workspaces therefore does not exist
-- while auth/000008 is running, and the foreign key can only be declared here.
--
-- ON DELETE CASCADE: deleting a workspace deletes its invites. An invite to a
-- workspace that no longer exists is not acceptable to anyone, and leaving the
-- rows behind would keep the partial unique index blocking a re-invite if the
-- workspace were recreated.

BEGIN;

SET LOCAL search_path = chat, auth, public;

-- Any pending invite that survived auth/000008 without a workspace would fail
-- the constraint below; auth/000008 revoked exactly those, so this is a
-- belt-and-braces guard against an out-of-order or partial apply.
ALTER TABLE auth.user_invites
    ADD CONSTRAINT user_invites_workspace_id_fkey
        FOREIGN KEY (workspace_id)
        REFERENCES chat.workspaces (id)
        ON DELETE CASCADE;

COMMIT;
