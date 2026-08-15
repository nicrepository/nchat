-- 000006_link_scan_jobs.down.sql
--
-- Dropping the queue discards in-flight scans. That is correct for a rollback:
-- the verdicts it held were an optimisation over asking the provider again, and
-- nothing else references the table.

BEGIN;

DROP TABLE IF EXISTS files.link_scans;

COMMIT;
