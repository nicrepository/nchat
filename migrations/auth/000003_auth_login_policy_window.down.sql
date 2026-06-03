-- 000003_auth_login_policy_window.down.sql
-- Removes temporary login-attempt lockout policy fields.

BEGIN;

ALTER TABLE auth.auth_policy_settings
  DROP CONSTRAINT IF EXISTS auth_policy_settings_failed_login_lockout_check,
  DROP CONSTRAINT IF EXISTS auth_policy_settings_failed_login_window_check,
  DROP COLUMN IF EXISTS failed_login_lockout_minutes,
  DROP COLUMN IF EXISTS failed_login_window_minutes;

COMMIT;
