-- Issue #546: authoritative channel/group-call targets and orphan cleanup leases.

BEGIN;

ALTER TABLE chat.calls
    ADD COLUMN target_type TEXT,
    ADD COLUMN target_id UUID;

UPDATE chat.calls
SET target_type = 'user', target_id = callee_id;

ALTER TABLE chat.calls
    ALTER COLUMN target_type SET NOT NULL,
    ALTER COLUMN target_id SET NOT NULL,
    ALTER COLUMN callee_id DROP NOT NULL;

DO $$
DECLARE
    old_constraint TEXT;
BEGIN
    SELECT conname INTO old_constraint
    FROM pg_constraint
    WHERE conrelid = 'chat.calls'::regclass
      AND contype = 'c'
      AND pg_get_constraintdef(oid) LIKE '%caller_id <> callee_id%';
    IF old_constraint IS NOT NULL THEN
        EXECUTE format('ALTER TABLE chat.calls DROP CONSTRAINT %I', old_constraint);
    END IF;
END $$;

ALTER TABLE chat.calls
    ADD CONSTRAINT calls_target_check CHECK (
        (target_type = 'user' AND callee_id IS NOT NULL AND target_id = callee_id AND caller_id <> callee_id)
        OR
        (target_type IN ('channel', 'dm') AND callee_id IS NULL)
    );

CREATE UNIQUE INDEX calls_one_active_resource_idx
    ON chat.calls (workspace_id, target_type, target_id)
    WHERE target_type IN ('channel', 'dm') AND status = 'active';

CREATE TABLE chat.call_participant_leases (
    call_id UUID NOT NULL REFERENCES chat.calls (id) ON DELETE CASCADE,
    user_id UUID NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (call_id, user_id)
);

CREATE INDEX call_participant_leases_expiry_idx
    ON chat.call_participant_leases (expires_at, call_id);

COMMIT;
