-- 000002_refresh_token_reuse_detection.up.sql
-- Tracks refresh token rotation history so reused rotated tokens can revoke the session family.

BEGIN;

CREATE SCHEMA IF NOT EXISTS auth;
SET LOCAL search_path = auth, public;

CREATE TABLE auth.refresh_token_history (
    id                  UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    session_id          UUID        NOT NULL REFERENCES auth.user_sessions (id) ON DELETE CASCADE,
    refresh_token_hash  TEXT        NOT NULL UNIQUE,
    status              TEXT        NOT NULL,
    issued_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    rotated_at          TIMESTAMPTZ,
    revoked_at          TIMESTAMPTZ,
    reused_at           TIMESTAMPTZ,

    CONSTRAINT refresh_token_history_status_check
        CHECK (status IN ('active', 'rotated', 'revoked', 'reused'))
);

CREATE INDEX idx_refresh_token_history_session_status
    ON auth.refresh_token_history (session_id, status);
CREATE INDEX idx_refresh_token_history_issued_at
    ON auth.refresh_token_history (issued_at);

INSERT INTO auth.refresh_token_history (
    session_id,
    refresh_token_hash,
    status,
    issued_at,
    revoked_at
)
SELECT
    id,
    refresh_token_hash,
    CASE WHEN revoked_at IS NULL THEN 'active' ELSE 'revoked' END,
    created_at,
    revoked_at
FROM auth.user_sessions;

COMMIT;
