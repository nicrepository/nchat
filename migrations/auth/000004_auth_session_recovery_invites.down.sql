-- 000004_auth_session_recovery_invites.down.sql
-- Reverts configurable reset/invite token TTLs and encrypted email outbox token handoff.

BEGIN;

SET LOCAL search_path = auth, public;

DROP TABLE IF EXISTS auth.email_outbox;

ALTER TABLE auth.auth_policy_settings
    DROP CONSTRAINT IF EXISTS auth_policy_settings_pw_reset_ttl_check,
    DROP CONSTRAINT IF EXISTS auth_policy_settings_invite_ttl_check,
    DROP COLUMN IF EXISTS password_reset_token_ttl_minutes,
    DROP COLUMN IF EXISTS invite_token_ttl_hours;

COMMIT;
