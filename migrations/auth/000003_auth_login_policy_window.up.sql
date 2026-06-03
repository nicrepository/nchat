-- 000003_auth_login_policy_window.up.sql
-- Adds policy fields required for temporary login-attempt lockout.

BEGIN;

ALTER TABLE auth.auth_policy_settings
  ADD COLUMN failed_login_window_minutes INT NOT NULL DEFAULT 15,
  ADD COLUMN failed_login_lockout_minutes INT NOT NULL DEFAULT 15;

ALTER TABLE auth.auth_policy_settings
  ADD CONSTRAINT auth_policy_settings_failed_login_window_check
    CHECK (failed_login_window_minutes > 0),
  ADD CONSTRAINT auth_policy_settings_failed_login_lockout_check
    CHECK (failed_login_lockout_minutes > 0);

COMMIT;
