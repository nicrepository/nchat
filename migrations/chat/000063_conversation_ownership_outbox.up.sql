BEGIN;
CREATE TABLE chat.ownership_outbox (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    transaction_id BIGINT NOT NULL DEFAULT txid_current(),
    workspace_id UUID NOT NULL,
    conversation_kind TEXT NOT NULL CHECK (conversation_kind IN ('dm','channel')),
    conversation_id UUID NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    published_at TIMESTAMPTZ,
    UNIQUE(transaction_id, conversation_kind, conversation_id)
);
CREATE INDEX ownership_outbox_pending ON chat.ownership_outbox(id) WHERE published_at IS NULL;
CREATE FUNCTION chat.enqueue_ownership_change() RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
    IF (SELECT enabled FROM chat.ownership_rollout WHERE singleton) THEN
        INSERT INTO chat.ownership_outbox(workspace_id,conversation_kind,conversation_id)
        VALUES (NEW.workspace_id,NEW.conversation_kind,NEW.conversation_id)
        ON CONFLICT (transaction_id,conversation_kind,conversation_id) DO NOTHING;
    END IF;
    RETURN NULL;
END;
$$;
CREATE TRIGGER ownership_audit_outbox AFTER INSERT ON chat.ownership_audit
FOR EACH ROW EXECUTE FUNCTION chat.enqueue_ownership_change();
-- Access and membership changes invalidate projections even when another
-- owner remains and no succession audit is necessary.
CREATE FUNCTION chat.enqueue_ownership_resource(p_kind TEXT,p_id UUID,p_workspace UUID)
RETURNS VOID LANGUAGE plpgsql AS $$
BEGIN
    IF (SELECT enabled FROM chat.ownership_rollout WHERE singleton) THEN
        INSERT INTO chat.ownership_outbox(workspace_id,conversation_kind,conversation_id)
        VALUES (p_workspace,p_kind,p_id)
        ON CONFLICT (transaction_id,conversation_kind,conversation_id) DO NOTHING;
    END IF;
END;
$$;
CREATE FUNCTION chat.enqueue_ownership_membership() RETURNS TRIGGER LANGUAGE plpgsql AS $$
DECLARE
    resource RECORD;
    membership JSONB;
    v_kind TEXT;
    v_id UUID;
BEGIN
    IF TG_OP = 'DELETE' THEN membership := to_jsonb(OLD);
    ELSE membership := to_jsonb(NEW); END IF;
    v_kind := CASE WHEN TG_TABLE_NAME='dm_members' THEN 'dm' ELSE 'channel' END;
    v_id := COALESCE(membership->>'conversation_id',membership->>'channel_id')::UUID;
    FOR resource IN
        SELECT workspace_id FROM chat.dm_conversations WHERE v_kind='dm' AND id=v_id AND type='group'
        UNION ALL
        SELECT workspace_id FROM chat.channels WHERE v_kind='channel' AND id=v_id AND type='private'
    LOOP
        PERFORM chat.enqueue_ownership_resource(v_kind,v_id,resource.workspace_id);
    END LOOP;
    RETURN NULL;
END;
$$;
CREATE TRIGGER ownership_dm_invalidation AFTER INSERT OR UPDATE OR DELETE ON chat.dm_members
FOR EACH ROW EXECUTE FUNCTION chat.enqueue_ownership_membership();
CREATE TRIGGER ownership_channel_invalidation AFTER INSERT OR UPDATE OR DELETE ON chat.channel_members
FOR EACH ROW EXECUTE FUNCTION chat.enqueue_ownership_membership();
CREATE FUNCTION chat.enqueue_ownership_access() RETURNS TRIGGER LANGUAGE plpgsql AS $$
DECLARE
    uid UUID;
    ws UUID;
    resource RECORD;
    invalidated BOOLEAN;
BEGIN
    IF NOT (SELECT enabled FROM chat.ownership_rollout WHERE singleton) THEN RETURN NULL; END IF;
    IF TG_TABLE_SCHEMA='auth' THEN uid := OLD.id;
    ELSE uid := OLD.user_id; ws := OLD.workspace_id; END IF;
    IF TG_OP='DELETE' THEN invalidated := true;
    ELSIF TG_TABLE_SCHEMA='auth' THEN invalidated := OLD.status='active' AND (NEW.status<>'active' OR NEW.deleted_at IS NOT NULL);
    ELSE invalidated := OLD.status='active' AND NEW.status<>'active'; END IF;
    FOR resource IN
        SELECT 'channel'::TEXT AS kind,c.id,c.workspace_id,m.ownership_role AS role
        FROM chat.channels c JOIN chat.channel_members m ON m.channel_id=c.id
        WHERE c.type='private' AND m.user_id=uid AND (ws IS NULL OR c.workspace_id=ws)
        UNION ALL
        SELECT 'dm',d.id,d.workspace_id,m.ownership_role
        FROM chat.dm_conversations d JOIN chat.dm_members m ON m.conversation_id=d.id
        WHERE d.type='group' AND m.status='active' AND m.user_id=uid AND (ws IS NULL OR d.workspace_id=ws)
        ORDER BY kind,id
    LOOP
        PERFORM chat.enqueue_ownership_resource(resource.kind,resource.id,resource.workspace_id);
        IF invalidated THEN
            INSERT INTO chat.ownership_audit(workspace_id,conversation_kind,conversation_id,target_user_id,previous_role,new_role,reason)
            VALUES(resource.workspace_id,resource.kind,resource.id,uid,resource.role,NULL,'invalidation');
        END IF;
    END LOOP;
    RETURN NULL;
END;
$$;
CREATE TRIGGER ownership_account_invalidation AFTER UPDATE OF status,deleted_at OR DELETE ON auth.users
FOR EACH ROW EXECUTE FUNCTION chat.enqueue_ownership_access();
CREATE TRIGGER ownership_workspace_invalidation AFTER UPDATE OF status,role OR DELETE ON chat.workspace_members
FOR EACH ROW EXECUTE FUNCTION chat.enqueue_ownership_access();
COMMIT;
