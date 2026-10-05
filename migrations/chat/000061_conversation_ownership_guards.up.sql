BEGIN;

CREATE FUNCTION chat.lock_ownership_conversation(p_kind TEXT, p_id UUID)
RETURNS VOID LANGUAGE plpgsql AS $$
BEGIN
    IF p_kind = 'dm' THEN
        PERFORM id FROM chat.dm_conversations WHERE id = p_id FOR UPDATE;
    ELSIF p_kind = 'channel' THEN
        PERFORM id FROM chat.channels WHERE id = p_id FOR UPDATE;
    ELSE
        RAISE EXCEPTION 'invalid conversation kind' USING ERRCODE = '22023';
    END IF;
END;
$$;

-- Call before taking account/workspace-membership locks. Stable order is shared
-- by auth/admin invalidation and chat mutations touching more than one resource.
CREATE FUNCTION chat.lock_user_ownership_conversations(p_user UUID, p_workspace UUID DEFAULT NULL)
RETURNS VOID LANGUAGE plpgsql AS $$
DECLARE
    resource RECORD;
BEGIN
    FOR resource IN
        SELECT 'channel'::TEXT AS kind, c.id
        FROM chat.channels c JOIN chat.channel_members m ON m.channel_id = c.id
        WHERE c.type = 'private' AND m.user_id = p_user
          AND (p_workspace IS NULL OR c.workspace_id = p_workspace)
        UNION
        SELECT 'dm', d.id FROM chat.dm_conversations d
        JOIN chat.dm_members m ON m.conversation_id = d.id
        WHERE d.type = 'group' AND m.user_id = p_user AND m.status = 'active'
          AND (p_workspace IS NULL OR d.workspace_id = p_workspace)
        ORDER BY kind, id
    LOOP
        PERFORM chat.lock_ownership_conversation(resource.kind, resource.id);
    END LOOP;
END;
$$;

CREATE FUNCTION chat.ensure_conversation_owner(p_kind TEXT, p_id UUID,
    p_succeed BOOLEAN, p_reason TEXT DEFAULT 'succession')
RETURNS VOID LANGUAGE plpgsql AS $$
DECLARE
    successor UUID;
BEGIN
    IF NOT (SELECT enabled FROM chat.ownership_rollout WHERE singleton) THEN RETURN; END IF;
    PERFORM chat.lock_ownership_conversation(p_kind, p_id);
    IF NOT EXISTS (SELECT 1 FROM chat.orphaned_private_conversations
        WHERE kind = p_kind AND conversation_id = p_id) THEN RETURN; END IF;
    IF p_succeed THEN
        SELECT user_id INTO successor FROM chat.active_ownership_participants
        WHERE kind = p_kind AND conversation_id = p_id AND NOT guest
        ORDER BY (role = 'admin') DESC, joined_at, user_id LIMIT 1;
        IF successor IS NOT NULL THEN
            PERFORM chat.assign_ownership(p_kind, p_id, successor, 'owner', NULL, p_reason);
            RETURN;
        END IF;
    END IF;
    RAISE EXCEPTION 'private conversation requires an active owner'
        USING ERRCODE = 'P0953', CONSTRAINT = 'private_conversation_owner_required';
END;
$$;

CREATE FUNCTION chat.ownership_membership_departure() RETURNS TRIGGER LANGUAGE plpgsql AS $$
DECLARE
    kind TEXT;
    conversation UUID;
BEGIN
    kind := CASE WHEN TG_TABLE_NAME = 'dm_members' THEN 'dm' ELSE 'channel' END;
    IF kind = 'dm' THEN conversation := OLD.conversation_id;
    ELSE conversation := OLD.channel_id; END IF;
    IF TG_OP = 'DELETE' THEN
        PERFORM chat.ensure_conversation_owner(kind, conversation, true);
    ELSIF OLD.status = 'active' AND NEW.status <> 'active' THEN
        PERFORM chat.ensure_conversation_owner(kind, conversation, true);
    END IF;
    RETURN NULL;
END;
$$;
CREATE TRIGGER dm_ownership_departure AFTER UPDATE OF status OR DELETE ON chat.dm_members
FOR EACH ROW EXECUTE FUNCTION chat.ownership_membership_departure();
CREATE TRIGGER channel_ownership_departure AFTER DELETE ON chat.channel_members
FOR EACH ROW EXECUTE FUNCTION chat.ownership_membership_departure();

-- Commit protection also covers raw SQL, creation, resurrection and demotion.
-- It never silently promotes a replacement for a simple role demotion.
CREATE FUNCTION chat.check_ownership_membership() RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
    IF TG_TABLE_NAME = 'dm_members' THEN
        IF TG_OP <> 'INSERT' THEN
            PERFORM chat.ensure_conversation_owner('dm', OLD.conversation_id, false);
        END IF;
        IF TG_OP <> 'DELETE' THEN
            PERFORM chat.ensure_conversation_owner('dm', NEW.conversation_id, false);
        END IF;
    ELSE
        IF TG_OP <> 'INSERT' THEN
            PERFORM chat.ensure_conversation_owner('channel', OLD.channel_id, false);
        END IF;
        IF TG_OP <> 'DELETE' THEN
            PERFORM chat.ensure_conversation_owner('channel', NEW.channel_id, false);
        END IF;
    END IF;
    RETURN NULL;
END;
$$;
CREATE CONSTRAINT TRIGGER dm_ownership_commit
AFTER INSERT OR UPDATE OR DELETE ON chat.dm_members DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION chat.check_ownership_membership();
CREATE CONSTRAINT TRIGGER channel_ownership_commit
AFTER INSERT OR UPDATE OR DELETE ON chat.channel_members DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION chat.check_ownership_membership();

CREATE FUNCTION chat.ownership_access_changed() RETURNS TRIGGER LANGUAGE plpgsql AS $$
DECLARE
    uid UUID;
    ws UUID;
    resource RECORD;
BEGIN
    IF NOT (SELECT enabled FROM chat.ownership_rollout WHERE singleton) THEN RETURN NULL; END IF;
    IF TG_TABLE_SCHEMA = 'auth' THEN uid := OLD.id;
    ELSE uid := OLD.user_id; ws := OLD.workspace_id; END IF;
    PERFORM chat.lock_user_ownership_conversations(uid, ws);
    FOR resource IN
        SELECT 'channel'::TEXT AS kind, c.id FROM chat.channels c
        JOIN chat.channel_members m ON m.channel_id = c.id
        WHERE m.user_id = uid AND (ws IS NULL OR c.workspace_id = ws) AND c.type = 'private'
        UNION
        SELECT 'dm', d.id FROM chat.dm_conversations d
        JOIN chat.dm_members m ON m.conversation_id = d.id
        WHERE m.user_id = uid AND (ws IS NULL OR d.workspace_id = ws) AND d.type = 'group'
        ORDER BY kind, id
    LOOP
        PERFORM chat.ensure_conversation_owner(resource.kind, resource.id, true, 'invalidation');
    END LOOP;
    RETURN NULL;
END;
$$;
CREATE TRIGGER ownership_account_access AFTER UPDATE OF status, deleted_at OR DELETE ON auth.users
FOR EACH ROW EXECUTE FUNCTION chat.ownership_access_changed();
CREATE TRIGGER ownership_workspace_access AFTER UPDATE OF status, role OR DELETE ON chat.workspace_members
FOR EACH ROW EXECUTE FUNCTION chat.ownership_access_changed();
COMMIT;
