-- 000022_workspace_moderator_and_guest_channel_scope.down.sql
-- Reverts RF-74 to the pre-existing role model.
--
-- The UPDATE is required and is not silent: the restored CHECK does not accept
-- 'moderator', so any workspace moderator has to become something the old
-- schema can hold. The rule is 'member' — the nearest role that carries no
-- management authority at all. Demoting is the only direction a rollback may
-- take; mapping a moderator to 'admin' would leave the workspace with more
-- administrators than it ever granted, and deleting the membership row would
-- destroy data a rollback has no business destroying.
--
-- Restoring the old function body is what returns guests to seeing every public
-- channel. That is a widening of access, which is exactly why it belongs in the
-- down migration and nowhere else: rolling back RF-74 rolls back guest
-- isolation with it. chat.channel_members rows written for guests while the
-- feature was live are kept — they stay valid memberships under either policy.

BEGIN;

UPDATE chat.workspace_members
SET role = 'member'
WHERE role = 'moderator';

ALTER TABLE chat.workspace_members
    DROP CONSTRAINT workspace_members_role_check;

ALTER TABLE chat.workspace_members
    ADD CONSTRAINT workspace_members_role_check
        CHECK (role IN ('owner', 'admin', 'member', 'guest'));

CREATE OR REPLACE FUNCTION chat.channel_visible_to_user(p_channel_id UUID, p_user_id UUID)
RETURNS boolean
LANGUAGE sql
STABLE
AS $$
    SELECT EXISTS (
        SELECT 1
        FROM chat.channels c
        LEFT JOIN chat.channel_members cm
          ON cm.channel_id = c.id AND cm.user_id = p_user_id
        WHERE c.id = p_channel_id
          AND (c.is_general = true OR c.type = 'public' OR cm.user_id IS NOT NULL)
    );
$$;

COMMIT;
