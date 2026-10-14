BEGIN;

-- The compatibility release understands both formats. Shadow values remain the
-- source understood by its rollback build after normalized roles are enabled.
CREATE FUNCTION chat.sync_ownership_membership() RETURNS TRIGGER LANGUAGE plpgsql AS $$
DECLARE
    active BOOLEAN;
BEGIN
    IF TG_TABLE_NAME = 'dm_members' THEN
        IF NOT EXISTS (SELECT 1 FROM chat.dm_conversations WHERE id=NEW.conversation_id AND type='group') THEN RETURN NEW; END IF;
    ELSE
        IF NOT EXISTS (SELECT 1 FROM chat.channels WHERE id=NEW.channel_id AND type='private') THEN RETURN NEW; END IF;
    END IF;
    IF TG_TABLE_NAME = 'dm_members' AND TG_OP = 'UPDATE' THEN
        IF OLD.status <> 'active' AND NEW.status = 'active' THEN
            NEW.joined_at := clock_timestamp();
            NEW.ownership_role := 'member';
            NEW.role := 'member';
        END IF;
    END IF;
    IF TG_OP = 'INSERT' THEN
        NEW.ownership_role := COALESCE(NEW.ownership_role,
            CASE WHEN NEW.role = 'moderator' THEN 'admin' ELSE NEW.role END);
    END IF;
    SELECT enabled INTO active FROM chat.ownership_rollout WHERE singleton;
    IF active AND NEW.ownership_role IS NOT NULL THEN
        NEW.role := NEW.ownership_role;
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER dm_ownership_sync BEFORE INSERT OR UPDATE ON chat.dm_members
FOR EACH ROW EXECUTE FUNCTION chat.sync_ownership_membership();
CREATE TRIGGER channel_ownership_sync BEFORE INSERT OR UPDATE ON chat.channel_members
FOR EACH ROW EXECUTE FUNCTION chat.sync_ownership_membership();

CREATE FUNCTION chat.bootstrap_ownership_membership() RETURNS TRIGGER LANGUAGE plpgsql AS $$
DECLARE
    v_kind TEXT;
    conversation UUID;
    successor UUID;
BEGIN
    IF TG_TABLE_NAME = 'dm_members' THEN v_kind := 'dm'; conversation := NEW.conversation_id;
    ELSE v_kind := 'channel'; conversation := NEW.channel_id; END IF;
    PERFORM chat.lock_ownership_conversation(v_kind, conversation);
    IF NOT EXISTS (SELECT 1 FROM chat.orphaned_private_conversations o
        WHERE o.kind = v_kind AND o.conversation_id = conversation) THEN RETURN NULL; END IF;
    SELECT p.user_id INTO successor FROM chat.active_ownership_participants p
    WHERE p.kind = v_kind AND p.conversation_id = conversation AND NOT p.guest
    ORDER BY (p.user_id = p.created_by) DESC, (p.role = 'admin') DESC,
        p.joined_at, p.user_id LIMIT 1;
    IF successor IS NOT NULL THEN
        PERFORM chat.assign_ownership(v_kind, conversation, successor, 'owner', NULL, 'backfill');
    END IF;
    PERFORM chat.ensure_conversation_owner(v_kind, conversation, false);
    RETURN NULL;
END;
$$;
CREATE CONSTRAINT TRIGGER dm_ownership_bootstrap AFTER INSERT ON chat.dm_members
DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION chat.bootstrap_ownership_membership();
CREATE CONSTRAINT TRIGGER channel_ownership_bootstrap AFTER INSERT ON chat.channel_members
DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION chat.bootstrap_ownership_membership();

CREATE FUNCTION chat.check_ownership_resource() RETURNS TRIGGER LANGUAGE plpgsql AS $$
DECLARE
    resource RECORD;
BEGIN
    IF TG_TABLE_NAME = 'dm_conversations' THEN
        PERFORM chat.ensure_conversation_owner('dm', NEW.id, false);
    ELSIF TG_TABLE_NAME = 'channels' THEN
        PERFORM chat.ensure_conversation_owner('channel', NEW.id, false);
    ELSE
        FOR resource IN SELECT kind, conversation_id FROM chat.orphaned_private_conversations
            WHERE workspace_id = NEW.id ORDER BY kind, conversation_id
        LOOP
            PERFORM chat.ensure_conversation_owner(resource.kind, resource.conversation_id, false);
        END LOOP;
    END IF;
    RETURN NULL;
END;
$$;
CREATE CONSTRAINT TRIGGER dm_ownership_resource AFTER UPDATE ON chat.dm_conversations
DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION chat.check_ownership_resource();
CREATE CONSTRAINT TRIGGER channel_ownership_resource AFTER UPDATE ON chat.channels
DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION chat.check_ownership_resource();
CREATE CONSTRAINT TRIGGER workspace_ownership_resource AFTER UPDATE ON chat.workspaces
DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION chat.check_ownership_resource();
COMMIT;
