-- 000022_workspace_moderator_and_guest_channel_scope.up.sql
-- RF-74 (issue #142): the full role model, and the two schema-level changes it
-- needs.
--
-- 1. A real workspace-level 'moderator'.
--
-- Until now chat.workspace_members.role accepted owner/admin/member/guest and
-- 'moderator' existed only on chat.channel_members, as a per-channel role.
-- SECURITY.md records the consequence: RF-17 asked for "Admin and Moderator"
-- and got owner/admin, because treating "moderates one channel" as "may
-- restructure the workspace" would have been a privilege escalation, and
-- inventing a workspace role nothing could hold would have been dead code
-- widening a security constraint. RF-74 is the requirement that creates the
-- role for real, so the constraint widens here and the two named seams
-- (domain.CanManageChannelCategories, domain.CanManageChannelMembers) widen
-- with it.
--
-- The per-channel 'moderator' on chat.channel_members is deliberately left
-- exactly as it is. The two roles remain different objects in different scopes;
-- nothing in this migration or in the code above it reads one as the other.
--
-- No existing row changes. 'moderator' is added to the accepted set and nothing
-- is reclassified into it: a workspace gets a moderator only when somebody
-- writes that value deliberately.
--
-- 2. Guest is scoped to the channels it was explicitly added to.
--
-- chat.channel_visible_to_user (migration 000007) already existed as the shared
-- "may this user see this channel" predicate, but it only knew about the
-- channel: general or public was visible to anyone, and only a private channel
-- consulted chat.channel_members. That is the correct answer for owner, admin,
-- moderator and member, and the wrong one for a guest, whose whole definition
-- in RF-74 is that workspace membership grants no channel access on its own.
--
-- Making this function role-aware is what enforces that everywhere at once. It
-- is the single definition of channel read access shared by chat-service,
-- file-service and media-service — the same database, the same rule — so the
-- listing, direct access by ID, message reads, pins, favorites, reactions,
-- attachment downloads and the WebSocket subscription authorizer all move
-- together instead of drifting apart in eleven inline copies of a predicate.
--
-- The role test is an allowlist rather than a test for the guest role alone.
-- A role this function does not recognise is denied rather than treated as a
-- full member, so widening the CHECK above can never silently widen read access
-- as a side effect.
--
-- Workspace membership status is checked here too, even though every caller
-- also joins chat.workspace_members with status = 'active'. A predicate that
-- decides access must be safe to call on its own; the redundancy fails closed.
--
-- The `c.is_general = true OR` disjunct of the previous body is gone, and this
-- changes nothing: migration 000002 constrains general channels with
-- CHECK (NOT is_general OR (type = 'public' AND status = 'active')), so a
-- general channel is a public channel by construction. Dropping the redundant
-- test is what lets domain.CanReadChannel state the identical rule in Go
-- without a clause that could only ever describe a row the schema forbids.

BEGIN;

ALTER TABLE chat.workspace_members
    DROP CONSTRAINT workspace_members_role_check;

ALTER TABLE chat.workspace_members
    ADD CONSTRAINT workspace_members_role_check
        CHECK (role IN ('owner', 'admin', 'moderator', 'member', 'guest'));

CREATE OR REPLACE FUNCTION chat.channel_visible_to_user(p_channel_id UUID, p_user_id UUID)
RETURNS boolean
LANGUAGE sql
STABLE
AS $$
    SELECT EXISTS (
        SELECT 1
        FROM chat.channels c
        JOIN chat.workspace_members wm
          ON wm.workspace_id = c.workspace_id
         AND wm.user_id = p_user_id
         AND wm.status = 'active'
        LEFT JOIN chat.channel_members cm
          ON cm.channel_id = c.id AND cm.user_id = p_user_id
        WHERE c.id = p_channel_id
          AND (
                -- An explicit membership is sufficient for every role, and is
                -- the only thing that is sufficient for a guest.
                cm.user_id IS NOT NULL
                OR (
                    wm.role IN ('owner', 'admin', 'moderator', 'member')
                    AND c.type = 'public'
                )
              )
    );
$$;

COMMIT;
