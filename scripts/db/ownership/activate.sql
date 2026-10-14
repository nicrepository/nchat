-- Explicit operator action; never part of automatic deploy.
\set ON_ERROR_STOP on
\if :{?legacy_retired}
\else
\echo 'legacy_retired is required'
DO $$ BEGIN RAISE EXCEPTION 'activation blocked: required operational parameter missing'; END $$;
\endif
\if :{?rollback_target_sha}
\else
\echo 'rollback_target_sha is required'
DO $$ BEGIN RAISE EXCEPTION 'activation blocked: required operational parameter missing'; END $$;
\endif
\if :{?retirement_evidence}
\else
\echo 'retirement_evidence is required'
DO $$ BEGIN RAISE EXCEPTION 'activation blocked: required operational parameter missing'; END $$;
\endif
BEGIN;
SET TRANSACTION ISOLATION LEVEL READ COMMITTED;
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';
SELECT set_config('nchat.ownership_legacy_retired', :'legacy_retired', true) AS legacy_setting,
    set_config('nchat.ownership_rollback_target_sha', :'rollback_target_sha', true) AS target_setting,
    set_config('nchat.ownership_retirement_evidence', :'retirement_evidence', true) AS evidence_setting
\gset
DO $$
DECLARE
    changed INTEGER;
BEGIN
    changed := chat.prepare_conversation_ownership_activation(
        current_setting('nchat.ownership_legacy_retired')::BOOLEAN,
        current_setting('nchat.ownership_rollback_target_sha'),
        current_setting('nchat.ownership_retirement_evidence'));
    IF changed >= 0 THEN
        -- These statements execute only through explicit, acknowledged activation.
        ALTER TABLE chat.dm_members DROP CONSTRAINT dm_members_role_check;
        ALTER TABLE chat.dm_members ADD CONSTRAINT dm_members_role_check
            CHECK (role IN ('member', 'admin', 'owner'));
        ALTER TABLE chat.channel_members DROP CONSTRAINT channel_members_role_check;
        ALTER TABLE chat.channel_members ADD CONSTRAINT channel_members_role_check
            CHECK (role IN ('member', 'moderator', 'admin', 'owner'));
        UPDATE chat.dm_members m SET role = m.ownership_role
        FROM chat.dm_conversations d
        WHERE d.id = m.conversation_id AND d.type = 'group'
            AND m.ownership_role IS NOT NULL AND m.role IS DISTINCT FROM m.ownership_role;
        UPDATE chat.channel_members m SET role = m.ownership_role
        FROM chat.channels c
        WHERE c.id = m.channel_id AND c.type = 'private'
            AND m.ownership_role IS NOT NULL AND m.role IS DISTINCT FROM m.ownership_role;
        UPDATE chat.ownership_rollout
        SET enabled = true, legacy_retired_at = now(),
            rollback_target_sha = current_setting('nchat.ownership_rollback_target_sha'),
            retirement_evidence = current_setting('nchat.ownership_retirement_evidence')
        WHERE singleton;
    END IF;
    RAISE NOTICE 'owners_assigned=%', greatest(changed, 0);
END;
$$;
SELECT enabled, legacy_retired_at, rollback_target_sha, retirement_evidence
FROM chat.ownership_rollout WHERE singleton;
COMMIT;
