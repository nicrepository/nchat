-- 000009_oidc_app_context.up.sql
-- Binds each OIDC login run to the NChat application that started it
-- (issue #578).
--
-- Why the column exists: the chat app and the administrative console are served
-- from different origins, so the identity provider must be given a different
-- redirect URI for each. The browser leaves NChat at /auth/oidc/keycloak/login
-- and comes back at the callback, and nothing it carries in between can be
-- trusted to say where it came from — a returning request is exactly what an
-- attacker controls.
--
-- The context is therefore written here, next to the state and nonce hashes, in
-- the same row the callback already consumes atomically. It stores a label from
-- a closed set, never a URL: the redirect URI is resolved server-side from that
-- label, so no value in this table can become a redirect target.
--
-- The default keeps every row that predates this migration meaning what it
-- meant: the chat application.

BEGIN;

SET LOCAL search_path = auth, public;

ALTER TABLE auth.oidc_auth_requests
    ADD COLUMN app_context TEXT NOT NULL DEFAULT 'chat';

ALTER TABLE auth.oidc_auth_requests
    ADD CONSTRAINT oidc_auth_requests_app_context_check
    CHECK (app_context IN ('chat', 'admin'));

COMMIT;
