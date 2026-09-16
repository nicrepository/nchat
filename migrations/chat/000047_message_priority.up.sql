BEGIN;

-- Message priority (issue #821).
--
-- The axis is the author's own claim about their message — standard, important,
-- urgent — and it is only that. It grants nothing: `urgent` does not widen what
-- its sender may read or post, and no authorization predicate anywhere reads
-- this column. A notification policy is free to consult it later and free to
-- ignore it; persisting the fact is this migration's whole job.
--
-- NOT NULL DEFAULT 'standard' rather than a nullable column, because "no
-- priority" is not a state this product has. Every message written before this
-- column existed was a standard one, and saying so in the schema is what keeps
-- every reader from having to spell out a COALESCE it could forget.
--
-- No table rewrite: PostgreSQL 11+ stores an ADD COLUMN ... DEFAULT as a
-- catalogue-level default and materialises it on the next write of each row, so
-- this is a metadata change on a table that grows with every message ever sent.
-- The ACCESS EXCLUSIVE lock is held for that catalogue write only.
--
-- TEXT with a CHECK rather than an enum type, matching kind, status and
-- link_safety_state on this same table: adding a value to a CHECK is an
-- ordinary migration, while ALTER TYPE ... ADD VALUE cannot run inside the
-- transaction every migration here is required to be wrapped in.
ALTER TABLE chat.messages
    ADD COLUMN priority TEXT NOT NULL DEFAULT 'standard';

-- The database is the last line, not the only one: the service refuses an
-- unknown priority before it ever gets here. This is what makes an invalid
-- value unreachable through any path at all — a future importer, a repair
-- script, a psql session — rather than only through the endpoint that is
-- guarded today.
--
-- NOT VALID: every existing row carries the default above and therefore already
-- satisfies it, but validating here would scan the whole table under ACCESS
-- EXCLUSIVE. Migration 000048 performs that scan under SHARE UPDATE EXCLUSIVE,
-- which blocks nothing an application does — the same two-step 000038/000039
-- and 000040/000041 use. Enforcement of new rows starts the instant this
-- commits; only the proof about old ones is deferred.
ALTER TABLE chat.messages
    ADD CONSTRAINT messages_priority_check
    CHECK (priority IN ('standard', 'important', 'urgent'))
    NOT VALID;

COMMIT;
