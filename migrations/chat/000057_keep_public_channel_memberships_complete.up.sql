BEGIN;

-- Keep creation correct during a blue/green rollout too: an older service slot
-- may create a public channel after migration 000056 ran but before the new
-- application version has replaced it.
CREATE FUNCTION chat.populate_public_channel_members()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
BEGIN
    IF NEW.type = 'public' AND NEW.status = 'active' THEN
        WITH inserted AS (
            INSERT INTO chat.channel_members (channel_id, user_id, role)
            SELECT NEW.id, wm.user_id, 'member'
            FROM chat.workspace_members wm
            WHERE wm.workspace_id = NEW.workspace_id
              AND wm.status = 'active'
              AND wm.role IN ('owner', 'admin', 'moderator', 'member')
            ON CONFLICT (channel_id, user_id) DO NOTHING
            RETURNING channel_id, user_id
        )
        INSERT INTO chat.migration_000056_public_channel_memberships (channel_id, user_id)
        SELECT channel_id, user_id FROM inserted
        ON CONFLICT (channel_id, user_id) DO NOTHING;
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER channels_populate_public_members
AFTER INSERT ON chat.channels
FOR EACH ROW
EXECUTE FUNCTION chat.populate_public_channel_members();

-- A person who joins or is reactivated later must also belong to every public
-- channel. Guests remain invite-only.
CREATE FUNCTION chat.populate_workspace_member_public_channels()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
BEGIN
    IF NEW.status = 'active'
       AND NEW.role IN ('owner', 'admin', 'moderator', 'member') THEN
        WITH inserted AS (
            INSERT INTO chat.channel_members (channel_id, user_id, role)
            SELECT c.id, NEW.user_id, 'member'
            FROM chat.channels c
            JOIN chat.workspaces w
              ON w.id = c.workspace_id
             AND w.status = 'active'
            WHERE c.workspace_id = NEW.workspace_id
              AND c.status = 'active'
              AND c.type = 'public'
            ON CONFLICT (channel_id, user_id) DO NOTHING
            RETURNING channel_id, user_id
        )
        INSERT INTO chat.migration_000056_public_channel_memberships (channel_id, user_id)
        SELECT channel_id, user_id FROM inserted
        ON CONFLICT (channel_id, user_id) DO NOTHING;
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER workspace_members_populate_public_channels
AFTER INSERT OR UPDATE OF status, role ON chat.workspace_members
FOR EACH ROW
EXECUTE FUNCTION chat.populate_workspace_member_public_channels();

-- Close the migration-to-trigger rollout window and record these repairs in
-- 000056's audit table so its rollback remains exact.
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
SELECT channel_id, user_id FROM inserted
ON CONFLICT (channel_id, user_id) DO NOTHING;

COMMIT;
