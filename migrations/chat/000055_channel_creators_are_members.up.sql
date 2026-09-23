BEGIN;

-- A channel creator is a participant of the conversation, matching the group
-- contract. Older public channels recorded created_by without materializing
-- the corresponding channel_members row, so their creator disappeared from
-- roster, member_count and mentions. Only currently eligible identities are
-- repaired; inactive/deleted users remain excluded by the normal policy.
CREATE TABLE chat.migration_000055_channel_creator_memberships (
    channel_id UUID NOT NULL,
    user_id UUID NOT NULL,
    PRIMARY KEY (channel_id, user_id)
);

WITH inserted AS (
    INSERT INTO chat.channel_members (channel_id, user_id, role)
    SELECT c.id, c.created_by, 'member'
    FROM chat.channels c
    JOIN chat.workspaces w
      ON w.id = c.workspace_id
     AND w.status = 'active'
    JOIN chat.workspace_members wm
      ON wm.workspace_id = c.workspace_id
     AND wm.user_id = c.created_by
     AND wm.status = 'active'
    JOIN auth.users u
      ON u.id = c.created_by
     AND u.status = 'active'
     AND u.deleted_at IS NULL
    WHERE c.status = 'active'
      AND c.created_by IS NOT NULL
    ON CONFLICT (channel_id, user_id) DO NOTHING
    RETURNING channel_id, user_id
)
INSERT INTO chat.migration_000055_channel_creator_memberships (channel_id, user_id)
SELECT channel_id, user_id FROM inserted;

COMMIT;
