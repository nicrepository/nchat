-- 000008_link_scan_inconclusive.down.sql
-- Reverses 000008 only while no terminal inconclusive rows exist. Turning one
-- back into submit_pending would manufacture a new provider submission.

BEGIN;

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM files.link_scans WHERE state = 'inconclusive') THEN
        RAISE EXCEPTION 'cannot roll back link scan inconclusive state while terminal rows exist';
    END IF;
END
$$;

DROP TRIGGER link_scans_inconclusive_terminal_guard ON files.link_scans;
DROP FUNCTION files.reject_inconclusive_link_scan_update();

DROP INDEX files.idx_files_link_scans_due;

CREATE INDEX idx_files_link_scans_due
    ON files.link_scans (next_attempt_at NULLS FIRST, created_at)
    WHERE state <> 'done';

ALTER TABLE files.link_scans
    DROP CONSTRAINT link_scans_state_check;

ALTER TABLE files.link_scans
    ADD CONSTRAINT link_scans_state_check
    CHECK (state IN ('submit_pending', 'submitting', 'submit_uncertain', 'polling', 'done'));

COMMIT;
