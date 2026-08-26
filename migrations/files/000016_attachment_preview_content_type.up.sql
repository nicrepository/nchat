-- 000016_attachment_preview_content_type.up.sql
-- Preview de planilhas/CSV: records what kind of bytes a preview object holds.
--
-- Every preview this service has ever produced, before this migration, is a
-- JPEG raster — the whole RF-31 pipeline and 000015's multi-page extension
-- both assume it implicitly. A spreadsheet/CSV preview is not a raster, it is
-- bounded table data, so the one fact this migration adds is the one thing
-- that was previously assumed rather than recorded: what content type the
-- stored bytes actually are.
--
-- # Why a column on both tables, not a third preview table
--
-- A page row (000015) and a sheet row differ only in what their bytes mean —
-- the object's identity, its encryption, its lease, its serving path are all
-- identical regardless of content. Splitting that into a second table would
-- fork MarkPreviewReady, GetPreviewPage, the HTTP dispatch and the frontend
-- fetch into two implementations that must stay in lockstep for a fact that
-- fits in one column. A closed CHECK, exactly like attachments_preview_status_
-- check (000003), is what keeps a third content type in the future an
-- explicit migration rather than a string anyone can write.
--
-- # Why no backfill
--
-- DEFAULT 'image/jpeg' is not a placeholder for existing rows, it is their
-- true historical value: every preview object stored before this migration
-- was produced by the image or PDF renderer, and nothing else. Every ALTER
-- below is additive and NOT NULL-with-DEFAULT, so a file-service binary that
-- predates this migration keeps writing valid rows unmodified — it simply
-- never learns that a preview can be anything but a JPEG.
--
-- application/vnd.nchat.preview-sheet+json (not a bare application/json) is
-- deliberately a private, versioned type: a client dispatches on it without
-- ambiguity, and it is this service's own bounded shape (columns/rows/
-- truncation flags — see internal/preview/csv.go and xlsx.go), never a
-- promise to serve arbitrary JSON.

BEGIN;

ALTER TABLE files.attachments
    ADD COLUMN preview_content_type TEXT NOT NULL DEFAULT 'image/jpeg';

ALTER TABLE files.attachments
    ADD CONSTRAINT attachments_preview_content_type_check CHECK (
        preview_content_type IN ('image/jpeg', 'application/vnd.nchat.preview-sheet+json')
    );

ALTER TABLE files.attachment_preview_pages
    ADD COLUMN content_type TEXT NOT NULL DEFAULT 'image/jpeg';

ALTER TABLE files.attachment_preview_pages
    ADD CONSTRAINT attachment_preview_pages_content_type_check CHECK (
        content_type IN ('image/jpeg', 'application/vnd.nchat.preview-sheet+json')
    );

COMMIT;
