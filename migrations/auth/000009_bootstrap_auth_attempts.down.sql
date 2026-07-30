-- 000009_bootstrap_auth_attempts.down.sql
-- Drops the bootstrap rate-limit counter.
--
-- The table holds only transient counters for windows that expire on their own,
-- so nothing here is state anybody needs restored: rolling back returns the
-- endpoint to an unlimited one, which is the behaviour that preceded it.

BEGIN;

SET LOCAL search_path = auth, public;

DROP INDEX IF EXISTS auth.idx_bootstrap_auth_attempts_window;

DROP TABLE IF EXISTS auth.bootstrap_auth_attempts;

COMMIT;
