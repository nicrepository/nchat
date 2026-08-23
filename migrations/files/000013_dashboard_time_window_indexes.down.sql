-- 000013_dashboard_time_window_indexes.down.sql
-- Drops the BRIN indexes added for the Admin Console dashboard (issue #581).
--
-- Index-only, so the rollback loses no data and changes no constraint: the two
-- counters still return the same numbers, by scanning for them.

BEGIN;

SET LOCAL search_path = files, public;

DROP INDEX IF EXISTS files.idx_attachments_updated_at_brin;
DROP INDEX IF EXISTS files.idx_attachments_created_at_brin;

COMMIT;
