-- 000002_refresh_token_reuse_detection.down.sql
-- Drops only the refresh token history table for this migration.

BEGIN;

SET LOCAL search_path = auth, public;

DROP TABLE IF EXISTS auth.refresh_token_history;

COMMIT;
