-- 000036_dashboard_time_window_indexes.down.sql
-- Drops the BRIN index added for the Admin Console dashboard (issue #581).
--
-- Index-only, so the rollback loses no data and changes no constraint: the two
-- counters still return the same numbers, by scanning for them.

BEGIN;

SET LOCAL search_path = chat, public;

DROP INDEX IF EXISTS chat.idx_messages_created_at_brin;

COMMIT;
