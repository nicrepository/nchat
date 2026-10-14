BEGIN;
SET LOCAL lock_timeout = '5s';
CREATE OR REPLACE FUNCTION chat.enqueue_ownership_change() RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
    IF (SELECT enabled FROM chat.ownership_rollout WHERE singleton) THEN
        INSERT INTO chat.ownership_outbox(workspace_id,conversation_kind,conversation_id)
        VALUES (NEW.workspace_id,NEW.conversation_kind,NEW.conversation_id)
        ON CONFLICT (transaction_id,conversation_kind,conversation_id) DO NOTHING;
    END IF;
    RETURN NULL;
END;
$$;
DROP INDEX chat.ownership_outbox_ready;
ALTER TABLE chat.ownership_outbox
    DROP COLUMN last_failure,
    DROP COLUMN next_attempt_at,
    DROP COLUMN last_attempt_at,
    DROP COLUMN attempt_count;
ALTER TABLE chat.ownership_audit
    DROP COLUMN result,
    DROP COLUMN operation,
    DROP COLUMN source,
    DROP COLUMN conversation_type;
COMMIT;
