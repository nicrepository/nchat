-- 000004_object_cleanup_jobs.down.sql
-- Reverses 000004 by dropping the cleanup queue.
--
-- What the rollback costs, stated plainly: any job still in the table is
-- outstanding work — an object in SeaweedFS that nothing points at and that
-- this queue was going to remove. Dropping the table forgets those keys, and
-- the objects stay. They are unreferenced previews, not user data: no
-- attachment, no download and no preview can reach them, and nothing breaks.
-- But they are also not reclaimed, so an operator rolling back with a non-empty
-- queue should record what was in it first:
--
--   SELECT object_key FROM files.object_cleanup_jobs ORDER BY created_at;
--
-- The table is dropped rather than emptied, and the index goes with it.

BEGIN;

DROP TABLE IF EXISTS files.object_cleanup_jobs;

COMMIT;
