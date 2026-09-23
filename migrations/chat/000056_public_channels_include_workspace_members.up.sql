BEGIN;

-- Public channels are workspace-wide conversations. Materialize every active
-- non-guest workspace member so roster, member_count and member actions match
-- that scope. Guests remain explicit channel invitees (RF-74).
CREATE TABLE chat.migration_000056_public_channel_memberships (
    channel_id UUID NOT NULL,
    user_id UUID NOT NULL,
    PRIMARY KEY (channel_id, user_id)
);

WITH inserted AS (
    INSERT INTO chat.channel_members (channel_id, user_id, role)
    SELECT c.id, wm.user_id, 'member'
    FROM chat.channels c
    JOIN chat.workspaces w
      ON w.id = c.workspace_id
     AND w.status = 'active'
    JOIN chat.workspace_members wm
      ON wm.workspace_id = c.workspace_id
     AND wm.status = 'active'
     AND wm.role IN ('owner', 'admin', 'moderator', 'member')
    WHERE c.status = 'active'
      AND c.type = 'public'
    ON CONFLICT (channel_id, user_id) DO NOTHING
    RETURNING channel_id, user_id
)
INSERT INTO chat.migration_000056_public_channel_memberships (channel_id, user_id)
SELECT channel_id, user_id FROM inserted;

COMMIT;
