-- 000008_admin_console_rbac.down.sql
-- Drops only what 000008 created. The auth schema, auth.users and
-- auth.user_sessions are left untouched.

BEGIN;

SET LOCAL search_path = auth, public;

DROP INDEX IF EXISTS auth.idx_admin_audit_events_actor;
DROP INDEX IF EXISTS auth.idx_admin_audit_events_occurred;
DROP INDEX IF EXISTS auth.idx_admin_sessions_auth_session;
DROP INDEX IF EXISTS auth.idx_admin_sessions_user_revoked;

DROP TABLE IF EXISTS auth.admin_audit_events;
DROP TABLE IF EXISTS auth.admin_sessions;
DROP TABLE IF EXISTS auth.admin_principal_roles;
DROP TABLE IF EXISTS auth.admin_principals;
DROP TABLE IF EXISTS auth.admin_role_capabilities;
DROP TABLE IF EXISTS auth.admin_roles;

COMMIT;
