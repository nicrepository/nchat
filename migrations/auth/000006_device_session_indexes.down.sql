-- migrations/auth/000006_device_session_indexes.down.sql
-- Drops only the indexes added in 000006. Does not touch extensions.

BEGIN;

SET LOCAL search_path = auth, public;

DROP INDEX IF EXISTS auth.idx_user_sessions_user_created;
DROP INDEX IF EXISTS auth.idx_user_sessions_user_device_revoked;
DROP INDEX IF EXISTS auth.idx_user_devices_user_last_seen;

COMMIT;
