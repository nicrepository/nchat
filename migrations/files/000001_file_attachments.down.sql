-- 000001_file_attachments.down.sql
-- Reverses 000001 for this phase: the attachments table and the indexes that
-- belong to it are dropped together (indexes are owned by the table).
--
-- The files schema itself is intentionally left in place. Dropping a schema is
-- a database-wide side effect that would take any object another task has
-- created in it, and scripts/ci/migrations-check.sh rejects DROP SCHEMA in a
-- down migration for exactly that reason. An empty schema is inert.

BEGIN;

DROP TABLE IF EXISTS files.attachments;

COMMIT;
