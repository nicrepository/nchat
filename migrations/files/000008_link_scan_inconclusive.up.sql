-- 000008_link_scan_inconclusive.up.sql
-- RF-21 (issue #135): the same terminal-inconclusive correction as
-- chat/000026, applied to file-service's own queue.
--
-- A Cloudflare URL Scanner report that is terminal (task.status='finished') but
-- carries no usable verdict (task.success=false, or hasVerdicts=false) used to
-- be indistinguishable from a transient failure, so the worker polled a
-- finished scan forever. This adds the terminal state that answer maps to.
--
-- No change is needed to link_scans_verdict_check: its existing "not done ->
-- verdict and verdict_expires_at both NULL" branch already covers the new
-- state. There is no verdict text and no expiry for an inconclusive row.
--
-- Migrations run before the RollingUpdate. The trigger below is therefore live
-- before a new worker can write this state. An origin/develop worker still uses
-- `state <> 'done'`; if it selects an inconclusive row, PostgreSQL cancels that
-- UPDATE and returns no claimed job, preserving the UUID and attempt history.

BEGIN;

ALTER TABLE files.link_scans
    DROP CONSTRAINT link_scans_state_check;

ALTER TABLE files.link_scans
    ADD CONSTRAINT link_scans_state_check
    CHECK (state IN ('submit_pending', 'submitting', 'submit_uncertain', 'polling', 'done', 'inconclusive'));

CREATE FUNCTION files.reject_inconclusive_link_scan_update()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    RETURN NULL;
END
$$;

CREATE TRIGGER link_scans_inconclusive_terminal_guard
    BEFORE UPDATE ON files.link_scans
    FOR EACH ROW
    WHEN (OLD.state = 'inconclusive')
    EXECUTE FUNCTION files.reject_inconclusive_link_scan_update();

DROP INDEX files.idx_files_link_scans_due;

CREATE INDEX idx_files_link_scans_due
    ON files.link_scans (next_attempt_at NULLS FIRST, created_at)
    WHERE state IN ('submit_pending', 'submitting', 'submit_uncertain', 'polling');

COMMIT;
