-- 000012_admin_config_versioning.down.sql
-- Reverses the Configuration & Secrets Management schema (issue #580).
--
-- Dropping the history is the point of the down migration: it is the only
-- structure this migration created, and there is nothing in it that another
-- table depends on. The settings row itself is left with its values intact —
-- only the concurrency counter goes, because without the history there is
-- nothing for it to be compared against.

BEGIN;

SET LOCAL search_path = auth, public;

DROP TABLE IF EXISTS auth.admin_config_version_changes;
DROP TABLE IF EXISTS auth.admin_config_versions;

ALTER TABLE auth.auth_policy_settings
    DROP CONSTRAINT IF EXISTS auth_policy_settings_revision_check;

ALTER TABLE auth.auth_policy_settings
    DROP COLUMN IF EXISTS revision;

COMMIT;
