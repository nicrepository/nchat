-- 000016_attachment_preview_pages.down.sql
-- Reverses 000016: drops the per-page table and the page-count column.
--
-- Runs against a populated table on purpose, for the same reason 000003's
-- rollback does: nothing here is irreplaceable. Every attachment, its object,
-- its wrapped key, its scan state and its page-1 preview binding (still on
-- the parent row, untouched by this migration) all survive. A dropped page
-- row is a rendering this service produced and can produce again.
--
-- What this rollback cannot do is reach into SeaweedFS. The extra-page
-- objects under nchat/previews/ survive the DROP and become unreferenced,
-- because the only pointer to each of them was its row in
-- attachment_preview_pages. That is a storage cleanup, not a data loss, and
-- it is deliberately not automated from a migration — see 000003's rollback
-- for the same reasoning. Operators purge the prefix after rolling back.
--
-- After this runs, a previously multi-page attachment simply has one page
-- again — page 1, exactly as it did before 000016 — because the client reads
-- pageCount from the column this migration drops, and a build that predates
-- this migration never asked for more than one page in the first place.
--
-- Order: table (and its own constraints, dropped implicitly with it), then
-- the parent table's constraint, then its column — the same "widest thing
-- first" discipline 000003's rollback follows.

BEGIN;

DROP TABLE IF EXISTS files.attachment_preview_pages;

ALTER TABLE files.attachments
    DROP CONSTRAINT IF EXISTS attachments_preview_page_count_check;

ALTER TABLE files.attachments
    DROP COLUMN IF EXISTS preview_page_count;

COMMIT;
