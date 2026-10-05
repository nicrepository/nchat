-- 000065_user_presence.down.sql
-- Drops the manual presence states and last-seen instants. Both are
-- user-visible but recoverable: users fall back to automatic presence.
BEGIN;

DROP TABLE IF EXISTS chat.user_presence;

COMMIT;
