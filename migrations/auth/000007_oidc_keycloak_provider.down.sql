-- migrations/auth/000007_oidc_keycloak_provider.down.sql
-- Drops only objects introduced by 000007 and restores the prior OIDC subject constraint/index.
-- Does not drop extensions.

BEGIN;

SET LOCAL search_path = auth, public;

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM auth.users WHERE auth_source = 'oidc' LIMIT 1) THEN
        RAISE EXCEPTION 'Cannot roll back migration 000007: OIDC users exist. Migrate them first.';
    END IF;
END;
$$;

DROP TABLE IF EXISTS auth.oidc_exchange_codes;
DROP TABLE IF EXISTS auth.oidc_auth_requests;

DROP INDEX IF EXISTS auth.idx_users_oidc_provider_subject_unique;

ALTER TABLE auth.users
    DROP CONSTRAINT IF EXISTS users_external_provider_not_blank_check;

ALTER TABLE auth.users
    DROP CONSTRAINT IF EXISTS users_oidc_subject_check;

ALTER TABLE auth.users
    ADD CONSTRAINT users_oidc_subject_check
    CHECK (auth_source <> 'oidc' OR external_subject IS NOT NULL);

CREATE UNIQUE INDEX idx_users_oidc_subject_unique
    ON auth.users (external_subject)
    WHERE auth_source = 'oidc' AND external_subject IS NOT NULL;

ALTER TABLE auth.users
    DROP COLUMN IF EXISTS external_provider;

COMMIT;
