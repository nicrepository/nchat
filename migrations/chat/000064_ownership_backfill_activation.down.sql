-- Only a pre-activation schema downgrade is supported. Application rollback
-- after activation keeps this migration and both role representations intact.
BEGIN;
SET LOCAL lock_timeout = '5s';
-- Freeze the same write surface/order as backfill before checking rollback history.
LOCK TABLE chat.workspaces, chat.dm_conversations, chat.channels,
    chat.dm_members, chat.channel_members, chat.workspace_members, auth.users
    IN SHARE ROW EXCLUSIVE MODE;
LOCK TABLE chat.ownership_rollout IN ACCESS EXCLUSIVE MODE;
DO $$ BEGIN
    IF NOT EXISTS (SELECT 1 FROM chat.ownership_rollout WHERE singleton
            AND NOT enabled AND legacy_retired_at IS NULL
            AND rollback_target_sha IS NULL AND retirement_evidence IS NULL)
        OR EXISTS (SELECT 1 FROM chat.dm_members m JOIN chat.dm_conversations d
            ON d.id = m.conversation_id WHERE d.type = 'group' AND m.role <> 'member')
        OR EXISTS (SELECT 1 FROM chat.channel_members m JOIN chat.channels c
            ON c.id = m.channel_id WHERE c.type = 'private' AND m.role IN ('admin', 'owner')) THEN
        RAISE EXCEPTION 'activation history or normalized data: retain storage and rollback to the recorded compatible release';
    END IF;
END $$;
DROP FUNCTION chat.prepare_conversation_ownership_activation(BOOLEAN, TEXT, TEXT);
CREATE OR REPLACE FUNCTION chat.backfill_conversation_ownership() RETURNS INTEGER LANGUAGE plpgsql AS $$
DECLARE
    candidate RECORD;
    changed INTEGER := 0;
BEGIN
    -- Normalize shadow roles without producing new legacy values.
    UPDATE chat.channel_members m SET ownership_role =
        CASE WHEN m.role = 'moderator' THEN 'admin' ELSE m.role END
    FROM chat.channels c WHERE c.id = m.channel_id AND c.type = 'private'
        AND m.ownership_role IS NULL;
    UPDATE chat.dm_members m SET ownership_role = m.role
    FROM chat.dm_conversations d WHERE d.id = m.conversation_id AND d.type = 'group'
        AND m.ownership_role IS NULL;
    FOR candidate IN
        SELECT DISTINCT ON (p.kind, p.conversation_id) p.*
        FROM chat.active_ownership_participants p
        JOIN chat.orphaned_private_conversations o
            USING (kind, conversation_id, workspace_id)
        WHERE NOT p.guest
        ORDER BY p.kind, p.conversation_id,
            (p.user_id = p.created_by) DESC,
            (p.role = 'admin') DESC, p.joined_at, p.user_id
    LOOP
        PERFORM chat.assign_ownership(candidate.kind, candidate.conversation_id,
            candidate.user_id, 'owner', NULL, 'backfill');
        changed := changed + 1;
    END LOOP;
    RETURN changed;
END;
$$;
ALTER TABLE chat.ownership_rollout DROP COLUMN retirement_evidence;
ALTER TABLE chat.ownership_rollout DROP COLUMN rollback_target_sha;
COMMIT;
