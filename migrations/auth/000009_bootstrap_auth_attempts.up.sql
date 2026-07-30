-- 000009_bootstrap_auth_attempts.up.sql
-- Shared counter backing the rate limit on the bootstrap credential.
--
-- The bootstrap route authenticates a pre-shared secret that can mint an
-- invite conferring ownership of a workspace, so the number of guesses an
-- attacker gets has to be bounded. The existing limiters in this service are
-- in-process token buckets: with N replicas an attacker gets N times the
-- budget, and a restart resets it. A credential this strong a guess deserves a
-- counter that actually holds across replicas.
--
-- PostgreSQL rather than a cache because it is the only shared backend this
-- service has: auth-service carries no Valkey/Redis client, config or
-- connection. The volume is negligible — this counts failed guesses against one
-- endpoint, not request traffic.
--
-- A table of its own rather than auth.login_attempts: that table requires an
-- email on every row and feeds both the user-facing /auth/me/login-attempts
-- listing and the login lockout window. Synthetic rows for a credential that
-- has no user would corrupt both.

BEGIN;

SET LOCAL search_path = auth, public;

CREATE TABLE auth.bootstrap_auth_attempts (
    -- Namespaced, already-normalised counter key: "<namespace>:<client-ip>".
    -- Never the credential, and never anything derived from it.
    limiter_key  TEXT        NOT NULL,
    -- Fixed window start, truncated by the application to the configured
    -- window. A fixed window rather than a sliding one keeps this to a single
    -- upsert per attempt, with no per-attempt row to accumulate or prune.
    window_start TIMESTAMPTZ NOT NULL,
    attempts     INTEGER     NOT NULL DEFAULT 0,
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT bootstrap_auth_attempts_pkey PRIMARY KEY (limiter_key, window_start),
    CONSTRAINT bootstrap_auth_attempts_count_check CHECK (attempts >= 0)
);

-- Serves the sweep that discards windows already over. Without it the table
-- would grow one row per (IP, window) forever.
CREATE INDEX idx_bootstrap_auth_attempts_window
    ON auth.bootstrap_auth_attempts (window_start);

COMMIT;
