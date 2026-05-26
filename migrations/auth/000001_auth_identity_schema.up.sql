-- 000001_auth_identity_schema.up.sql
-- Auth identity data model: users, credentials, invites, sessions, devices,
-- login attempts, password reset tokens, and policy settings.
--
-- Security decisions:
--   - All token/password fields store hashes only (token_hash, password_hash)
--   - citext for email: case-insensitive comparison without normalisation burden
--   - inet type for IP addresses: native PostgreSQL CIDR-aware type
--   - auth schema keeps auth-service tables out of the public namespace
--   - Soft delete via deleted_at / anonymized_at; hard delete reserved for V1.0
--   - auth_policy_settings enforces a single-row config via CHECK (id = 1)

BEGIN;

CREATE SCHEMA IF NOT EXISTS auth;
SET LOCAL search_path = auth, public;

-- ---------------------------------------------------------------------------
-- Extensions
-- ---------------------------------------------------------------------------

CREATE EXTENSION IF NOT EXISTS pgcrypto WITH SCHEMA public;
CREATE EXTENSION IF NOT EXISTS citext WITH SCHEMA public;

-- ---------------------------------------------------------------------------
-- users
-- ---------------------------------------------------------------------------

CREATE TABLE auth.users (
    id                  UUID            PRIMARY KEY DEFAULT gen_random_uuid(),
    email               CITEXT          NOT NULL,
    display_name        TEXT            NOT NULL,
    full_name           TEXT,
    avatar_url          TEXT,
    status              TEXT            NOT NULL DEFAULT 'active',
    auth_source         TEXT            NOT NULL DEFAULT 'manual',
    external_subject    TEXT,
    email_verified_at   TIMESTAMPTZ,
    last_login_at       TIMESTAMPTZ,
    deleted_at          TIMESTAMPTZ,
    anonymized_at       TIMESTAMPTZ,
    created_at          TIMESTAMPTZ     NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ     NOT NULL DEFAULT now(),

    CONSTRAINT users_email_unique UNIQUE (email),
    CONSTRAINT users_status_check CHECK (status IN ('active', 'invited', 'suspended', 'locked', 'deleted')),
    CONSTRAINT users_auth_source_check CHECK (auth_source IN ('manual', 'oidc', 'imported')),
    CONSTRAINT users_oidc_subject_check CHECK (auth_source <> 'oidc' OR external_subject IS NOT NULL)
);

CREATE INDEX idx_users_status     ON auth.users (status);
CREATE INDEX idx_users_deleted_at ON auth.users (deleted_at);
CREATE UNIQUE INDEX idx_users_oidc_subject_unique
    ON auth.users (external_subject)
    WHERE auth_source = 'oidc' AND external_subject IS NOT NULL;

-- ---------------------------------------------------------------------------
-- user_password_credentials
-- ---------------------------------------------------------------------------

CREATE TABLE auth.user_password_credentials (
    user_id                 UUID        PRIMARY KEY REFERENCES auth.users (id) ON DELETE CASCADE,
    password_hash           TEXT        NOT NULL,
    password_changed_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    password_expires_at     TIMESTAMPTZ,
    must_change_password    BOOLEAN     NOT NULL DEFAULT false,
    created_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at              TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- ---------------------------------------------------------------------------
-- user_invites
-- ---------------------------------------------------------------------------

CREATE TABLE auth.user_invites (
    id                      UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    email                   CITEXT      NOT NULL,
    invited_by_user_id      UUID        REFERENCES auth.users (id) ON DELETE SET NULL,
    token_hash              TEXT        NOT NULL,
    status                  TEXT        NOT NULL DEFAULT 'pending',
    expires_at              TIMESTAMPTZ NOT NULL,
    accepted_at             TIMESTAMPTZ,
    accepted_by_user_id     UUID        REFERENCES auth.users (id) ON DELETE SET NULL,
    revoked_at              TIMESTAMPTZ,
    created_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at              TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT user_invites_token_hash_unique UNIQUE (token_hash),
    CONSTRAINT user_invites_status_check CHECK (status IN ('pending', 'accepted', 'expired', 'revoked'))
);

CREATE INDEX idx_user_invites_email_status ON auth.user_invites (email, status);

-- ---------------------------------------------------------------------------
-- user_devices
-- (declared before user_sessions so sessions can reference device_id)
-- ---------------------------------------------------------------------------

CREATE TABLE auth.user_devices (
    id                          UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id                     UUID        NOT NULL REFERENCES auth.users (id) ON DELETE CASCADE,
    device_fingerprint_hash     TEXT        NOT NULL,
    display_name                TEXT,
    platform                    TEXT,
    last_ip                     INET,
    first_seen_at               TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen_at                TIMESTAMPTZ NOT NULL DEFAULT now(),
    trusted_at                  TIMESTAMPTZ,
    revoked_at                  TIMESTAMPTZ,

    CONSTRAINT user_devices_user_fingerprint_unique UNIQUE (user_id, device_fingerprint_hash),
    CONSTRAINT user_devices_user_id_id_unique UNIQUE (user_id, id)
);

CREATE INDEX idx_user_devices_user_revoked ON auth.user_devices (user_id, revoked_at);

-- ---------------------------------------------------------------------------
-- user_sessions
-- ---------------------------------------------------------------------------

CREATE TABLE auth.user_sessions (
    id                      UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id                 UUID        NOT NULL REFERENCES auth.users (id) ON DELETE CASCADE,
    device_id               UUID,
    refresh_token_hash      TEXT        NOT NULL,
    ip_address              INET,
    user_agent              TEXT,
    created_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    idle_expires_at         TIMESTAMPTZ NOT NULL,
    absolute_expires_at     TIMESTAMPTZ,
    revoked_at              TIMESTAMPTZ,
    revoked_reason          TEXT,

    CONSTRAINT user_sessions_refresh_token_hash_unique UNIQUE (refresh_token_hash),
    CONSTRAINT user_sessions_user_device_fk
        FOREIGN KEY (user_id, device_id)
        REFERENCES auth.user_devices (user_id, id)
        ON DELETE SET NULL (device_id)
);

CREATE INDEX idx_user_sessions_user_revoked ON auth.user_sessions (user_id, revoked_at);
CREATE INDEX idx_user_sessions_user_device  ON auth.user_sessions (user_id, device_id);

-- ---------------------------------------------------------------------------
-- login_attempts
-- ---------------------------------------------------------------------------

CREATE TABLE auth.login_attempts (
    id              BIGSERIAL   PRIMARY KEY,
    user_id         UUID        REFERENCES auth.users (id) ON DELETE SET NULL,
    email           CITEXT      NOT NULL,
    success         BOOLEAN     NOT NULL,
    failure_reason  TEXT,
    ip_address      INET,
    user_agent      TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_login_attempts_email_time   ON auth.login_attempts (email, created_at DESC);
CREATE INDEX idx_login_attempts_user_time    ON auth.login_attempts (user_id, created_at DESC);

-- ---------------------------------------------------------------------------
-- password_reset_tokens
-- ---------------------------------------------------------------------------

CREATE TABLE auth.password_reset_tokens (
    id          UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id     UUID        NOT NULL REFERENCES auth.users (id) ON DELETE CASCADE,
    token_hash  TEXT        NOT NULL,
    expires_at  TIMESTAMPTZ NOT NULL,
    used_at     TIMESTAMPTZ,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT password_reset_tokens_token_hash_unique UNIQUE (token_hash)
);

CREATE INDEX idx_password_reset_tokens_user_used ON auth.password_reset_tokens (user_id, used_at);

-- ---------------------------------------------------------------------------
-- auth_policy_settings
-- Single-row table enforced by CHECK (id = 1).
-- ---------------------------------------------------------------------------

CREATE TABLE auth.auth_policy_settings (
    id                              SMALLINT    PRIMARY KEY DEFAULT 1,
    min_password_length             INT         NOT NULL DEFAULT 12,
    require_uppercase               BOOLEAN     NOT NULL DEFAULT true,
    require_lowercase               BOOLEAN     NOT NULL DEFAULT true,
    require_number                  BOOLEAN     NOT NULL DEFAULT true,
    require_symbol                  BOOLEAN     NOT NULL DEFAULT true,
    password_expiration_days        INT,
    failed_login_limit              INT         NOT NULL DEFAULT 5,
    session_idle_timeout_minutes    INT         NOT NULL DEFAULT 60,
    max_devices_per_user            INT         NOT NULL DEFAULT 5,
    created_at                      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at                      TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT auth_policy_settings_single_row CHECK (id = 1),
    CONSTRAINT auth_policy_settings_min_password_length_check CHECK (min_password_length > 0),
    CONSTRAINT auth_policy_settings_password_expiration_days_check CHECK (password_expiration_days IS NULL OR password_expiration_days > 0),
    CONSTRAINT auth_policy_settings_failed_login_limit_check CHECK (failed_login_limit > 0),
    CONSTRAINT auth_policy_settings_session_idle_timeout_check CHECK (session_idle_timeout_minutes > 0),
    CONSTRAINT auth_policy_settings_max_devices_check CHECK (max_devices_per_user > 0)
);

INSERT INTO auth.auth_policy_settings DEFAULT VALUES;

COMMIT;
