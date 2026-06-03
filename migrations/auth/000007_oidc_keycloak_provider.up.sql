-- migrations/auth/000007_oidc_keycloak_provider.up.sql
-- Adds Keycloak OIDC account linking and one-time OIDC callback state/exchange storage.
-- Token, state, nonce, and PKCE values are never stored raw.

BEGIN;

SET LOCAL search_path = auth, public;

ALTER TABLE auth.users
    ADD COLUMN external_provider TEXT;

ALTER TABLE auth.users
    DROP CONSTRAINT users_oidc_subject_check;

ALTER TABLE auth.users
    ADD CONSTRAINT users_oidc_subject_check
    CHECK (auth_source <> 'oidc' OR (external_provider IS NOT NULL AND external_subject IS NOT NULL));

ALTER TABLE auth.users
    ADD CONSTRAINT users_external_provider_not_blank_check
    CHECK (external_provider IS NULL OR btrim(external_provider) <> '');

DROP INDEX IF EXISTS auth.idx_users_oidc_subject_unique;

CREATE UNIQUE INDEX idx_users_oidc_provider_subject_unique
    ON auth.users (external_provider, external_subject)
    WHERE auth_source = 'oidc'
      AND external_provider IS NOT NULL
      AND external_subject IS NOT NULL;

CREATE TABLE auth.oidc_auth_requests (
    id                      UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    provider                TEXT        NOT NULL,
    state_hash              TEXT        NOT NULL UNIQUE,
    nonce_hash              TEXT        NOT NULL,
    pkce_verifier_encrypted TEXT        NOT NULL,
    redirect_after          TEXT,
    created_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at              TIMESTAMPTZ NOT NULL,
    used_at                 TIMESTAMPTZ,

    CONSTRAINT oidc_auth_requests_provider_check CHECK (provider IN ('keycloak')),
    CONSTRAINT oidc_auth_requests_state_hash_not_blank_check CHECK (btrim(state_hash) <> ''),
    CONSTRAINT oidc_auth_requests_nonce_hash_not_blank_check CHECK (btrim(nonce_hash) <> ''),
    CONSTRAINT oidc_auth_requests_expires_after_created_check CHECK (expires_at > created_at),
    CONSTRAINT oidc_auth_requests_redirect_after_internal_check
        CHECK (redirect_after IS NULL OR redirect_after LIKE '/%')
);

CREATE INDEX idx_oidc_auth_requests_provider_expires
    ON auth.oidc_auth_requests (provider, expires_at);

CREATE TABLE auth.oidc_exchange_codes (
    id                       UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    provider                 TEXT        NOT NULL,
    code_hash                TEXT        NOT NULL UNIQUE,
    access_value_encrypted   TEXT        NOT NULL,
    refresh_value_encrypted  TEXT        NOT NULL,
    bearer_scheme               TEXT        NOT NULL,
    expires_in               INT         NOT NULL,
    user_json                JSONB       NOT NULL,
    created_at               TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at               TIMESTAMPTZ NOT NULL,
    used_at                  TIMESTAMPTZ,

    CONSTRAINT oidc_exchange_codes_provider_check CHECK (provider IN ('keycloak')),
    CONSTRAINT oidc_exchange_codes_code_hash_not_blank_check CHECK (btrim(code_hash) <> ''),
    CONSTRAINT oidc_exchange_codes_bearer_scheme_check CHECK (bearer_scheme = 'Bearer'),
    CONSTRAINT oidc_exchange_codes_expires_in_check CHECK (expires_in > 0),
    CONSTRAINT oidc_exchange_codes_expires_after_created_check CHECK (expires_at > created_at)
);

CREATE INDEX idx_oidc_exchange_codes_provider_expires
    ON auth.oidc_exchange_codes (provider, expires_at);

COMMIT;
