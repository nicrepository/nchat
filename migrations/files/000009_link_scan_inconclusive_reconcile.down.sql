-- 000009_link_scan_inconclusive_reconcile.down.sql
-- Restores 000008's blanket refusal: an inconclusive row becomes immutable by
-- UPDATE again.
--
-- Safe to run at any time and in either order relative to the workers. It only
-- ever removes a permission, so the worst outcome is that a reconciliation which
-- did obtain a verdict cannot write it and the row stays inconclusive — the
-- fail-closed direction. Rows already reconciled to 'done' keep their verdict;
-- nothing here reopens them.

BEGIN;

DROP INDEX files.idx_files_link_scans_reconcile_due;

ALTER TABLE files.link_scans
    DROP COLUMN IF EXISTS next_reconcile_at;

ALTER TABLE files.link_scans
    DROP COLUMN IF EXISTS reconcile_attempts;

CREATE OR REPLACE FUNCTION files.reject_inconclusive_link_scan_update()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    RETURN NULL;
END
$$;

COMMIT;
