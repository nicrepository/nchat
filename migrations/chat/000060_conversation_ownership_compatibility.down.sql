BEGIN;
DO $$ BEGIN
    IF EXISTS(SELECT 1 FROM chat.ownership_rollout WHERE enabled) THEN
        RAISE EXCEPTION 'ownership is active: rollback to the compatibility binary without dropping storage';
    END IF;
END $$;
DROP FUNCTION IF EXISTS chat.backfill_conversation_ownership();
DROP FUNCTION IF EXISTS chat.assign_ownership(TEXT, UUID, UUID, TEXT, UUID, TEXT);
DROP VIEW IF EXISTS chat.orphaned_private_conversations;
DROP VIEW IF EXISTS chat.active_ownership_participants;
DROP TABLE IF EXISTS chat.ownership_requests;
DROP TABLE IF EXISTS chat.ownership_audit;
DROP TABLE IF EXISTS chat.ownership_rollout;
ALTER TABLE chat.channel_members DROP COLUMN IF EXISTS ownership_role;
ALTER TABLE chat.dm_members DROP COLUMN IF EXISTS ownership_role;
COMMIT;
