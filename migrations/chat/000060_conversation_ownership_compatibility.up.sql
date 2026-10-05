-- Compatibility release: legacy role values remain unchanged. Activation is a
-- separate operator action after retirement of every incompatible writer.
BEGIN;

ALTER TABLE chat.dm_members ADD COLUMN ownership_role TEXT
    CHECK (ownership_role IN ('owner', 'admin', 'member'));
ALTER TABLE chat.channel_members ADD COLUMN ownership_role TEXT
    CHECK (ownership_role IN ('owner', 'admin', 'member'));

CREATE TABLE chat.ownership_rollout (
    singleton BOOLEAN PRIMARY KEY DEFAULT true CHECK (singleton),
    enabled BOOLEAN NOT NULL DEFAULT false,
    legacy_retired_at TIMESTAMPTZ,
    CHECK (NOT enabled OR legacy_retired_at IS NOT NULL)
);
INSERT INTO chat.ownership_rollout (singleton) VALUES (true);

CREATE TABLE chat.ownership_audit (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    workspace_id UUID NOT NULL,
    conversation_kind TEXT NOT NULL CHECK (conversation_kind IN ('dm', 'channel')),
    conversation_id UUID NOT NULL,
    actor_user_id UUID,
    target_user_id UUID NOT NULL,
    previous_role TEXT,
    new_role TEXT,
    reason TEXT NOT NULL CHECK (reason IN ('backfill', 'manual', 'transfer', 'succession', 'invalidation')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX ownership_audit_conversation ON chat.ownership_audit
    (conversation_kind, conversation_id, id);

CREATE TABLE chat.ownership_requests (
    workspace_id UUID NOT NULL,
    conversation_kind TEXT NOT NULL CHECK (conversation_kind IN ('dm', 'channel')),
    conversation_id UUID NOT NULL,
    actor_user_id UUID NOT NULL,
    idempotency_key TEXT NOT NULL CHECK (length(idempotency_key) BETWEEN 1 AND 128),
    request_hash TEXT NOT NULL,
    response JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (workspace_id, conversation_kind, conversation_id, actor_user_id, idempotency_key)
);

-- The same eligibility predicate is used for bootstrap, diagnostics and
-- succession. Guests count as valid owners but never as automatic candidates.
CREATE VIEW chat.active_ownership_participants AS
SELECT 'dm'::text AS kind, d.id AS conversation_id, d.workspace_id,
       d.created_by, m.user_id, m.joined_at, wm.role = 'guest' AS guest,
       COALESCE(m.ownership_role, m.role) AS role
FROM chat.dm_conversations d
JOIN chat.dm_members m ON m.conversation_id = d.id AND m.status = 'active'
JOIN chat.workspaces w ON w.id = d.workspace_id AND w.status = 'active'
JOIN chat.workspace_members wm ON wm.workspace_id = d.workspace_id
    AND wm.user_id = m.user_id AND wm.status = 'active'
JOIN auth.users u ON u.id = m.user_id AND u.status = 'active' AND u.deleted_at IS NULL
WHERE d.type = 'group' AND d.status = 'active'
UNION ALL
SELECT 'channel', c.id, c.workspace_id, c.created_by, m.user_id, m.joined_at,
       wm.role = 'guest', COALESCE(m.ownership_role,
           CASE WHEN m.role = 'moderator' THEN 'admin' ELSE m.role END)
FROM chat.channels c
JOIN chat.channel_members m ON m.channel_id = c.id
JOIN chat.workspaces w ON w.id = c.workspace_id AND w.status = 'active'
JOIN chat.workspace_members wm ON wm.workspace_id = c.workspace_id
    AND wm.user_id = m.user_id AND wm.status = 'active'
JOIN auth.users u ON u.id = m.user_id AND u.status = 'active' AND u.deleted_at IS NULL
WHERE c.type = 'private' AND c.status = 'active';

CREATE VIEW chat.orphaned_private_conversations AS
SELECT kind, conversation_id, workspace_id, count(*) AS active_members
FROM chat.active_ownership_participants
GROUP BY kind, conversation_id, workspace_id
HAVING count(*) FILTER (WHERE role = 'owner') = 0;

CREATE FUNCTION chat.assign_ownership(p_kind TEXT, p_id UUID, p_user UUID,
    p_role TEXT, p_actor UUID, p_reason TEXT) RETURNS VOID LANGUAGE plpgsql AS $$
DECLARE
    previous TEXT;
    ws UUID;
BEGIN
    SELECT role, workspace_id INTO previous, ws
    FROM chat.active_ownership_participants
    WHERE kind = p_kind AND conversation_id = p_id AND user_id = p_user;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'ownership participant unavailable' USING ERRCODE = 'P0954';
    END IF;
    IF previous = p_role THEN RETURN; END IF;
    IF p_kind = 'dm' THEN
        UPDATE chat.dm_members SET ownership_role = p_role
        WHERE conversation_id = p_id AND user_id = p_user;
    ELSE
        UPDATE chat.channel_members SET ownership_role = p_role
        WHERE channel_id = p_id AND user_id = p_user;
    END IF;
    INSERT INTO chat.ownership_audit
        (workspace_id, conversation_kind, conversation_id, actor_user_id,
         target_user_id, previous_role, new_role, reason)
    VALUES (ws, p_kind, p_id, p_actor, p_user, previous, p_role, p_reason);
END;
$$;

CREATE FUNCTION chat.backfill_conversation_ownership() RETURNS INTEGER LANGUAGE plpgsql AS $$
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
SELECT chat.backfill_conversation_ownership();
COMMIT;
