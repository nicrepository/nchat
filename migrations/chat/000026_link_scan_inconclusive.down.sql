-- 000026_link_scan_inconclusive.down.sql
-- Reverses 000026 only while no terminal inconclusive rows exist. Requeueing
-- one would manufacture a new provider submission.

BEGIN;

-- An inconclusive scan is terminal. A rollback cannot safely invent a fresh
-- provider attempt, so refuse until an operator chooses an explicit migration
-- for those rows.
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM chat.link_scans WHERE status = 'inconclusive') THEN
        RAISE EXCEPTION 'cannot roll back link scan inconclusive state while terminal rows exist';
    END IF;
END
$$;

ALTER TABLE chat.message_publish_outbox
    DROP CONSTRAINT IF EXISTS message_publish_outbox_block_reason_presence_check;

ALTER TABLE chat.message_publish_outbox
    DROP CONSTRAINT IF EXISTS message_publish_outbox_block_reason_check;

ALTER TABLE chat.message_publish_outbox
    DROP COLUMN IF EXISTS block_reason;

ALTER TABLE chat.link_scans
    DROP CONSTRAINT link_scans_status_check;

ALTER TABLE chat.link_scans
    ADD CONSTRAINT link_scans_status_check
    CHECK (status IN ('pending', 'safe', 'malicious'));

DROP INDEX chat.idx_link_scans_decided_at;

CREATE INDEX idx_link_scans_decided_at
    ON chat.link_scans (decided_at)
    WHERE status <> 'pending';

COMMIT;
