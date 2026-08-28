-- 000025_link_scan_capacity.down.sql
-- Reverses 000025. The budget history is disposable — it is fixed-window
-- counters, not business state — and the submission-window columns fall back to
-- the previous behaviour, where an unconfirmed submission is indistinguishable
-- from one that never happened.

BEGIN;

DROP TABLE IF EXISTS chat.link_scan_budget_usage;

DROP INDEX IF EXISTS chat.idx_link_scans_submit_uncertain;

ALTER TABLE chat.link_scans
    DROP CONSTRAINT IF EXISTS link_scans_submit_attempt_check;

ALTER TABLE chat.link_scans
    DROP COLUMN IF EXISTS submit_generation,
    DROP COLUMN IF EXISTS submit_attempt_started_at;

COMMIT;
