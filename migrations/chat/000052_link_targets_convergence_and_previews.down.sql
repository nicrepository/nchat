-- 000052_link_targets_convergence_and_previews.down.sql
-- Reverses the issue #807 schema. Refuses to run while `unknown` targets
-- exist: the previous version's CHECK cannot represent them, and rewriting them
-- to `pending` would resurrect exactly the eternal-pending state this migration
-- exists to end.

BEGIN;

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM chat.link_scans WHERE status = 'unknown') THEN
        RAISE EXCEPTION 'cannot roll back link target convergence while unknown targets exist';
    END IF;
END
$$;

DROP INDEX IF EXISTS chat.idx_link_fanouts_due;
DROP TABLE IF EXISTS chat.link_fanouts;

DROP INDEX IF EXISTS chat.idx_link_previews_url;
DROP INDEX IF EXISTS chat.idx_link_previews_deadline;
DROP INDEX IF EXISTS chat.idx_link_previews_due;
DROP TABLE IF EXISTS chat.link_previews;

DROP INDEX IF EXISTS chat.idx_link_scans_deadline;

DROP INDEX chat.idx_link_scans_decided_at;
CREATE INDEX idx_link_scans_decided_at
    ON chat.link_scans (decided_at)
    WHERE status IN ('safe', 'malicious');

ALTER TABLE chat.link_scans
    DROP CONSTRAINT IF EXISTS link_scans_pending_deadline_check;

DROP TRIGGER IF EXISTS link_scans_pending_deadline ON chat.link_scans;
DROP FUNCTION IF EXISTS chat.link_scans_pending_deadline();

ALTER TABLE chat.link_scans
    DROP CONSTRAINT IF EXISTS link_scans_terminal_reason_check;

ALTER TABLE chat.link_scans
    DROP CONSTRAINT link_scans_status_check;

ALTER TABLE chat.link_scans
    ADD CONSTRAINT link_scans_status_check
    CHECK (status IN ('pending', 'safe', 'malicious', 'inconclusive'));

ALTER TABLE chat.link_scans
    DROP COLUMN IF EXISTS terminal_reason;

ALTER TABLE chat.link_scans
    DROP COLUMN IF EXISTS deadline_at;

COMMIT;
