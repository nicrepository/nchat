-- 000011_admin_audit_resource_index.down.sql
-- Drops the index added for the per-user audit history (issue #579).
--
-- Index-only, so the rollback loses no data and changes no constraint: the
-- filtered query still returns the same rows, by scanning for them.

BEGIN;

SET LOCAL search_path = auth, public;

DROP INDEX IF EXISTS auth.idx_admin_audit_events_resource;

COMMIT;
