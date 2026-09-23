-- 000058_link_scan_evidence_and_secondary.down.sql
--
-- Reverses issue #928's additions to chat.link_scans.
--
-- Losing evidence_expires_at makes every verdict expire on the local VerdictTTL
-- alone, which is the behaviour before this migration and is more permissive
-- only for a condemnation whose provider-stated expiry was shorter than fifteen
-- minutes. Losing the secondary lane abandons any verification outstanding at
-- the moment of the rollback; those targets keep their existing verdict and are
-- rechecked from scratch when it expires.
--
-- Losing secondary_generation abandons the attempt identity with the lane it
-- belonged to; nothing outlives the rollback that could write into a lane that
-- no longer exists.
--
-- Neither loss can turn a blocked link into a clickable one on its own: status
-- and decided_at, which is what freshVerdictSQL and every reader decide from,
-- are untouched.

BEGIN;

DROP INDEX IF EXISTS chat.idx_link_scans_evidence_expires_at;
DROP INDEX IF EXISTS chat.idx_link_scans_secondary_due;

ALTER TABLE chat.link_scans
    DROP CONSTRAINT IF EXISTS link_scans_secondary_pair_check;

ALTER TABLE chat.link_scans
    DROP COLUMN IF EXISTS secondary_generation;

ALTER TABLE chat.link_scans
    DROP COLUMN IF EXISTS secondary_scan_uuid;

ALTER TABLE chat.link_scans
    DROP COLUMN IF EXISTS secondary_due_at;

ALTER TABLE chat.link_scans
    DROP COLUMN IF EXISTS evidence_expires_at;

COMMIT;
