-- 000016_attachment_preview_content_type.down.sql
-- Reverses 000016: drops the content-type columns and their CHECKs.
--
-- Safe against a populated table for the same reason 000003's and 000015's
-- rollbacks are: nothing here is irreplaceable, every attachment's own object
-- and every existing JPEG preview survive untouched. After this runs, a
-- build that predates this migration reads every preview as the JPEG it has
-- always assumed — which is exactly correct for every row produced before
-- sheet previews existed, and simply stale (not lost, not corrupted) for any
-- sheet-kind preview produced in between: its object still exists in
-- SeaweedFS, it is just no longer labelled, so an operator rolling back after
-- shipping sheet previews should expect those attachments to need
-- regeneration once this migration is re-applied, not treat this as silent
-- data loss.
--
-- Order: constraints then columns, widest thing first, same discipline as
-- every rollback in this directory.

BEGIN;

ALTER TABLE files.attachment_preview_pages
    DROP CONSTRAINT IF EXISTS attachment_preview_pages_content_type_check;

ALTER TABLE files.attachment_preview_pages
    DROP COLUMN IF EXISTS content_type;

ALTER TABLE files.attachments
    DROP CONSTRAINT IF EXISTS attachments_preview_content_type_check;

ALTER TABLE files.attachments
    DROP COLUMN IF EXISTS preview_content_type;

COMMIT;
