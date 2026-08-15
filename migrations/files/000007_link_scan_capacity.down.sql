-- 000007_link_scan_capacity.down.sql
-- Reverses 000007. Any row still in one of the two new states is moved back to
-- submit_pending before the constraint is narrowed, which restores the previous
-- behaviour for it: an unconfirmed submission becomes indistinguishable from one
-- that never happened, and will be submitted again.

BEGIN;

DROP TABLE IF EXISTS files.link_scan_budget_usage;

DROP INDEX IF EXISTS files.idx_files_link_scans_submit_uncertain;

UPDATE files.link_scans
   SET state = 'submit_pending', updated_at = now()
 WHERE state IN ('submitting', 'submit_uncertain');

ALTER TABLE files.link_scans
    DROP CONSTRAINT IF EXISTS link_scans_submit_attempt_check;

ALTER TABLE files.link_scans
    DROP COLUMN IF EXISTS submit_generation,
    DROP COLUMN IF EXISTS submit_attempt_started_at;

ALTER TABLE files.link_scans
    DROP CONSTRAINT link_scans_state_check;

ALTER TABLE files.link_scans
    ADD CONSTRAINT link_scans_state_check
    CHECK (state IN ('submit_pending', 'polling', 'done'));

COMMIT;
