-- Expand only: constraints and role values remain legacy-compatible.
-- Contract DDL lives in scripts/db/ownership/activate.sql and is never run by deploy.
BEGIN;
SET LOCAL lock_timeout = '5s';

ALTER TABLE chat.ownership_rollout ADD COLUMN rollback_target_sha TEXT
    CHECK (rollback_target_sha ~ '^[a-f0-9]{40}$');
ALTER TABLE chat.ownership_rollout ADD COLUMN retirement_evidence TEXT
    CHECK (retirement_evidence ~ '^[A-Za-z0-9][A-Za-z0-9_./:#-]{0,255}$');

CREATE OR REPLACE FUNCTION chat.backfill_conversation_ownership()
RETURNS INTEGER LANGUAGE plpgsql AS $$
DECLARE
    changed INTEGER;
BEGIN
    -- One operational snapshot: membership/eligibility cannot change mid-backfill.
    -- SHARE ROW EXCLUSIVE permits reads and blocks concurrent writers.
    LOCK TABLE chat.workspaces, chat.dm_conversations, chat.channels,
        chat.dm_members, chat.channel_members, chat.workspace_members, auth.users
        IN SHARE ROW EXCLUSIVE MODE;

    WITH normalized AS (
        UPDATE chat.dm_members m SET ownership_role = m.role
        FROM chat.dm_conversations d
        WHERE d.id = m.conversation_id AND d.type = 'group'
            AND m.ownership_role IS NULL
        RETURNING d.workspace_id, d.id AS conversation_id, m.user_id, m.ownership_role
    )
    INSERT INTO chat.ownership_audit
        (workspace_id, conversation_kind, conversation_id, target_user_id,
         previous_role, new_role, reason)
    SELECT workspace_id, 'dm', conversation_id, user_id, NULL, ownership_role, 'backfill'
    FROM normalized;

    WITH normalized AS (
        UPDATE chat.channel_members m SET ownership_role =
            CASE WHEN m.role = 'moderator' THEN 'admin' ELSE m.role END
        FROM chat.channels c
        WHERE c.id = m.channel_id AND c.type = 'private'
            AND m.ownership_role IS NULL
        RETURNING c.workspace_id, c.id AS conversation_id, m.user_id, m.ownership_role
    )
    INSERT INTO chat.ownership_audit
        (workspace_id, conversation_kind, conversation_id, target_user_id,
         previous_role, new_role, reason)
    SELECT workspace_id, 'channel', conversation_id, user_id, NULL, ownership_role, 'backfill'
    FROM normalized;

    WITH candidates AS MATERIALIZED (
        SELECT DISTINCT ON (p.kind, p.conversation_id) p.*
        FROM chat.active_ownership_participants p
        JOIN chat.orphaned_private_conversations o
            USING (kind, conversation_id, workspace_id)
        WHERE NOT p.guest
        ORDER BY p.kind, p.conversation_id,
            (p.user_id = p.created_by) DESC,
            (p.kind = 'channel' AND p.role = 'admin') DESC,
            p.joined_at ASC, p.user_id ASC
    ), dm_promoted AS (
        UPDATE chat.dm_members m SET ownership_role = 'owner'
        FROM candidates p
        WHERE p.kind = 'dm' AND m.conversation_id = p.conversation_id
            AND m.user_id = p.user_id AND m.ownership_role IS DISTINCT FROM 'owner'
        RETURNING p.workspace_id, p.kind, p.conversation_id, p.user_id, p.role
    ), channel_promoted AS (
        UPDATE chat.channel_members m SET ownership_role = 'owner'
        FROM candidates p
        WHERE p.kind = 'channel' AND m.channel_id = p.conversation_id
            AND m.user_id = p.user_id AND m.ownership_role IS DISTINCT FROM 'owner'
        RETURNING p.workspace_id, p.kind, p.conversation_id, p.user_id, p.role
    )
    INSERT INTO chat.ownership_audit
        (workspace_id, conversation_kind, conversation_id, target_user_id,
         previous_role, new_role, reason)
    SELECT workspace_id, kind, conversation_id, user_id, role, 'owner', 'backfill'
    FROM dm_promoted
    UNION ALL
    SELECT workspace_id, kind, conversation_id, user_id, role, 'owner', 'backfill'
    FROM channel_promoted;
    GET DIAGNOSTICS changed = ROW_COUNT;
    -- Preserve the existing return contract: owners assigned, not normalized rows.
    RETURN changed;
END;
$$;

CREATE FUNCTION chat.prepare_conversation_ownership_activation(
    p_legacy_retired BOOLEAN, p_rollback_target_sha TEXT, p_retirement_evidence TEXT)
RETURNS INTEGER LANGUAGE plpgsql AS $$
DECLARE
    rollout chat.ownership_rollout%ROWTYPE;
    changed INTEGER;
BEGIN
    IF p_legacy_retired IS DISTINCT FROM true THEN
        RAISE EXCEPTION 'activation blocked: incompatible active/rollback releases must be retired';
    END IF;
    IF p_rollback_target_sha IS NULL OR p_rollback_target_sha !~ '^[a-f0-9]{40}$'
        OR p_retirement_evidence IS NULL
        OR p_retirement_evidence !~ '^[A-Za-z0-9][A-Za-z0-9_./:#-]{0,255}$' THEN
        RAISE EXCEPTION 'activation blocked: full rollback SHA and non-sensitive evidence reference required';
    END IF;

    -- Same order as the backfill; freeze eligibility while allowing account reads.
    -- Only membership tables need ACCESS EXCLUSIVE for the activation DDL.
    LOCK TABLE chat.workspaces, chat.dm_conversations, chat.channels
        IN SHARE ROW EXCLUSIVE MODE;
    LOCK TABLE chat.dm_members, chat.channel_members IN ACCESS EXCLUSIVE MODE;
    LOCK TABLE chat.workspace_members, auth.users IN SHARE ROW EXCLUSIVE MODE;
    SELECT * INTO rollout FROM chat.ownership_rollout WHERE singleton FOR UPDATE;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'activation blocked: rollout record missing';
    END IF;
    IF rollout.rollback_target_sha IS NOT NULL
        AND rollout.rollback_target_sha <> p_rollback_target_sha THEN
        RAISE EXCEPTION 'activation blocked: rollback target cannot change';
    END IF;
    IF rollout.enabled THEN
        IF rollout.rollback_target_sha IS NULL OR rollout.retirement_evidence IS NULL
            OR rollout.legacy_retired_at IS NULL THEN
            RAISE EXCEPTION 'activation blocked: existing activation requires operational evidence repair';
        END IF;
        IF EXISTS (SELECT 1 FROM chat.orphaned_private_conversations) THEN
            RAISE EXCEPTION 'activation blocked: private conversations without an eligible owner';
        END IF;
        RETURN -1; -- Already active: the caller must not repeat DDL or data writes.
    END IF;
    IF rollout.legacy_retired_at IS NOT NULL OR rollout.rollback_target_sha IS NOT NULL
        OR rollout.retirement_evidence IS NOT NULL THEN
        RAISE EXCEPTION 'activation blocked: inconsistent disabled rollout';
    END IF;

    changed := chat.backfill_conversation_ownership();
    IF EXISTS (SELECT 1 FROM chat.orphaned_private_conversations) THEN
        RAISE EXCEPTION 'activation blocked: private conversations without an eligible owner';
    END IF;
    -- Locks remain held until the caller commits activation DDL/data/state.
    RETURN changed;
END;
$$;

COMMIT;
