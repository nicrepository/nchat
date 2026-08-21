-- 000010_link_fetch_denylist.down.sql
-- Removes the shared fetch denylist.
--
-- This is the one rollback in this feature that genuinely loses a security fact:
-- the rows here record URLs a component proved malicious, and nothing else in the
-- system retains that across the two independent verdict stores. After this runs,
-- a URL condemned by chat-service is once again invisible to file-service's
-- preview gate until file-service scans it itself.
--
-- The per-service verdict rows are untouched, so nothing becomes *more*
-- permissive immediately: files.link_scans rows the writers expired stay expired
-- and are re-scanned on demand. The loss is prospective, and it is the reason to
-- roll application code back first and this only if the table itself is the
-- problem.

BEGIN;

DROP TRIGGER IF EXISTS link_scans_denylist_clearance_guard ON files.link_scans;
DROP FUNCTION IF EXISTS files.reject_denylisted_link_clearance();
DROP TABLE IF EXISTS files.link_fetch_denylist;

COMMIT;
