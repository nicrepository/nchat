-- 000001_auth_identity_schema.down.sql
-- Drops all tables created in the up migration in reverse dependency order.

BEGIN;

DROP TABLE IF EXISTS auth_policy_settings;
DROP TABLE IF EXISTS password_reset_tokens;
DROP TABLE IF EXISTS login_attempts;
DROP TABLE IF EXISTS user_sessions;
DROP TABLE IF EXISTS user_devices;
DROP TABLE IF EXISTS user_invites;
DROP TABLE IF EXISTS user_password_credentials;
DROP TABLE IF EXISTS users;

DROP EXTENSION IF EXISTS citext;
DROP EXTENSION IF EXISTS pgcrypto;

COMMIT;
