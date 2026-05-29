-- 000004_auth_session_recovery_invites.up.sql
-- Adds configurable reset/invite token TTLs and encrypted email outbox token handoff.

BEGIN;

SET LOCAL search_path = auth, public;

ALTER TABLE auth.auth_policy_settings
    ADD COLUMN password_reset_token_ttl_minutes INT NOT NULL DEFAULT 60,
    ADD COLUMN invite_token_ttl_hours           INT NOT NULL DEFAULT 72;

ALTER TABLE auth.auth_policy_settings
    ADD CONSTRAINT auth_policy_settings_pw_reset_ttl_check
        CHECK (password_reset_token_ttl_minutes > 0),
    ADD CONSTRAINT auth_policy_settings_invite_ttl_check
        CHECK (invite_token_ttl_hours > 0);

CREATE TABLE auth.email_outbox (
    id             UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    kind           TEXT        NOT NULL,
    to_email       TEXT        NOT NULL,
    subject        TEXT        NOT NULL,
    template_key   TEXT        NOT NULL,
    reset_token_id UUID        REFERENCES auth.password_reset_tokens (id) ON DELETE CASCADE,
    invite_id      UUID        REFERENCES auth.user_invites (id) ON DELETE CASCADE,
    user_id        UUID        REFERENCES auth.users (id) ON DELETE SET NULL,
    payload        JSONB       NOT NULL DEFAULT '{}'::jsonb,
    status         TEXT        NOT NULL DEFAULT 'pending',
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    sent_at        TIMESTAMPTZ,
    attempts       INT         NOT NULL DEFAULT 0,
    last_error     TEXT,

    CONSTRAINT email_outbox_kind_check
        CHECK (kind IN ('password_reset', 'invite')),
    CONSTRAINT email_outbox_status_check
        CHECK (status IN ('pending', 'sent', 'failed')),
    CONSTRAINT email_outbox_reference_check
        CHECK (
            (kind = 'password_reset' AND reset_token_id IS NOT NULL AND invite_id IS NULL)
            OR
            (kind = 'invite' AND invite_id IS NOT NULL AND reset_token_id IS NULL)
        )
);

CREATE INDEX idx_email_outbox_status_created ON auth.email_outbox (status, created_at);
CREATE INDEX idx_email_outbox_reset_token_id ON auth.email_outbox (reset_token_id) WHERE reset_token_id IS NOT NULL;
CREATE INDEX idx_email_outbox_invite_id ON auth.email_outbox (invite_id) WHERE invite_id IS NOT NULL;

COMMIT;
