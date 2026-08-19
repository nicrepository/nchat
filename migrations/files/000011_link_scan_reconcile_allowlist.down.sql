-- 000011_link_scan_reconcile_allowlist.down.sql
-- Restores 000009's wider `inconclusive -> done` branch.
--
-- This rollback makes the guard *less* strict, which is the direction a rollback
-- of a hardening change necessarily goes. Every reopening path stays refused —
-- that part is unchanged between the two versions — but the exit transition goes
-- back to permitting an UPDATE that also rewrites the row's identity along the
-- way. Nothing in the application does that, so the practical effect is nil; the
-- reason to prefer 000011 is that the database should not be relying on the
-- application's good behaviour.

BEGIN;

-- The trigger depends on the function, so both are dropped and recreated rather
-- than the function being replaced in place. Recreated with the same name,
-- timing and WHEN clause 000008 established, so the guard is continuous: there is
-- no instant inside this transaction in which an inconclusive row is unguarded.
DROP TRIGGER link_scans_inconclusive_terminal_guard ON files.link_scans;
DROP FUNCTION files.reject_inconclusive_link_scan_update();

CREATE FUNCTION files.reject_inconclusive_link_scan_update()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NEW.state = 'inconclusive' THEN
        IF NEW.url_digest IS DISTINCT FROM OLD.url_digest
           OR NEW.canonical_url IS DISTINCT FROM OLD.canonical_url
           OR NEW.scan_uuid IS DISTINCT FROM OLD.scan_uuid
           OR NEW.verdict IS DISTINCT FROM OLD.verdict
           OR NEW.verdict_expires_at IS DISTINCT FROM OLD.verdict_expires_at
           OR NEW.attempts IS DISTINCT FROM OLD.attempts
           OR NEW.next_attempt_at IS DISTINCT FROM OLD.next_attempt_at
           OR NEW.lease_until IS DISTINCT FROM OLD.lease_until
           OR NEW.submit_attempt_started_at IS DISTINCT FROM OLD.submit_attempt_started_at
           OR NEW.submit_generation IS DISTINCT FROM OLD.submit_generation
           OR NEW.created_at IS DISTINCT FROM OLD.created_at THEN
            RETURN NULL;
        END IF;
        IF NEW.reconcile_attempts = OLD.reconcile_attempts
           AND NEW.next_reconcile_at IS NOT DISTINCT FROM OLD.next_reconcile_at THEN
            RETURN NULL;
        END IF;
        IF NEW.reconcile_attempts < OLD.reconcile_attempts THEN
            RETURN NULL;
        END IF;
        RETURN NEW;
    END IF;

    IF NEW.state = 'done'
       AND NEW.scan_uuid IS NOT DISTINCT FROM OLD.scan_uuid
       AND NEW.scan_uuid IS NOT NULL
       AND NEW.verdict IS NOT NULL
       AND NEW.attempts >= OLD.attempts THEN
        RETURN NEW;
    END IF;

    RETURN NULL;
END
$$;

CREATE TRIGGER link_scans_inconclusive_terminal_guard
    BEFORE UPDATE ON files.link_scans
    FOR EACH ROW
    WHEN (OLD.state = 'inconclusive')
    EXECUTE FUNCTION files.reject_inconclusive_link_scan_update();

COMMIT;
