-- 000001_auth_identity_schema.down.sql
-- Drops all tables created in the up migration in reverse dependency order.
-- Database-wide extensions are intentionally retained because they may predate
-- this migration or be shared by later schemas in the same database.

BEGIN;

SET LOCAL search_path = auth, public;

DROP TABLE IF EXISTS auth.auth_policy_settings;
DROP TABLE IF EXISTS auth.password_reset_tokens;
DROP TABLE IF EXISTS auth.login_attempts;
DROP TABLE IF EXISTS auth.user_sessions;
DROP TABLE IF EXISTS auth.user_devices;
DROP TABLE IF EXISTS auth.user_invites;
DROP TABLE IF EXISTS auth.user_password_credentials;
DROP TABLE IF EXISTS auth.users;

COMMIT;
