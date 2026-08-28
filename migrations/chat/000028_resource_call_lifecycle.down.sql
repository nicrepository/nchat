-- Issue #546 rollback: remove resource-call rows before restoring direct-only invariants.

BEGIN;

DROP TABLE IF EXISTS chat.call_participant_leases;
DROP INDEX IF EXISTS chat.calls_one_active_resource_idx;

DELETE FROM chat.calls WHERE target_type IN ('channel', 'dm');

ALTER TABLE chat.calls
    DROP CONSTRAINT IF EXISTS calls_target_check,
    ALTER COLUMN callee_id SET NOT NULL,
    ADD CONSTRAINT calls_direct_participants_check CHECK (caller_id <> callee_id),
    DROP COLUMN target_id,
    DROP COLUMN target_type;

COMMIT;
