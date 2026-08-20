-- 000009_oidc_app_context.down.sql
-- Drops only the column and constraint added by 000009. In-flight login runs
-- lose their context and fall back to the chat application, which is the
-- behaviour that existed before the column.

BEGIN;

SET LOCAL search_path = auth, public;

ALTER TABLE auth.oidc_auth_requests
    DROP CONSTRAINT IF EXISTS oidc_auth_requests_app_context_check;

ALTER TABLE auth.oidc_auth_requests
    DROP COLUMN IF EXISTS app_context;

COMMIT;
