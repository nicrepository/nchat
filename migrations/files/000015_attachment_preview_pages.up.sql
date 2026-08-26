-- 000015_attachment_preview_pages.up.sql
-- Fase 1 of task #494: multi-page PDF previews.
--
-- 000003 gave every attachment at most one preview object, addressed by the
-- columns on the row itself. A PDF preview today is that same single object:
-- page one, rendered once, stored once. This migration lifts the "one" without
-- disturbing anything 000003 built, because the asymmetry it introduces is
-- deliberate and has to be read as such, not discovered by diffing two tables.
--
-- # Why page one stays exactly where 000003 put it
--
-- files.attachments.preview_object_id (and its four sibling columns) keeps
-- meaning "this attachment's first preview page", unchanged. Every reader of
-- it today — GetPreview, the chat thumbnail, RF-31's whole delivery path —
-- keeps working without a line of change. The alternative, moving every page
-- including the first into a child table, would touch that entire path for a
-- feature that, for the overwhelming majority of previewable attachments
-- (every image, and any one-page PDF), produces exactly one page: the common
-- case would pay a JOIN it never needed. The cost of keeping the asymmetry is
-- that "attachment_id + page_number" is split across two tables instead of
-- one — page 1 implicit on the parent row, pages 2..N explicit in the child —
-- and that has to be a code-reviewed decision, not a discovered one.
--
-- # Why a genuine child table, and not another job-on-row column set
--
-- 000003 and 000005 both put their job on the row because each of those
-- features is inherently one-to-one: one job, one outcome, one attachment.
-- This feature is inherently one-to-many — an attachment can have anywhere
-- from zero extra pages (an image, a one-page PDF) to MaxPreviewPDFPages minus
-- one — and a one-to-many fact belongs in a child table, not in N speculative
-- column sets on the parent. files.attachment_preview_pages is that table.
--
-- # Why there is still no 'processing' state, expressed structurally instead
-- of with a status column
--
-- 000003's discipline was: no column here ever means "in progress", because a
-- lease is what recovers a crashed attempt and a processing flag is just
-- something else that can get stuck. This table keeps that discipline by
-- having no status column at all: a page row is written once, by the same
-- statement that flips the parent's preview_status to 'ready', with every
-- column it needs to be servable. A page that failed to render simply has no
-- row — there is nothing to distinguish "rendering" from "not yet rendered"
-- because this table only ever holds finished, servable pages.
--
-- # Why preview_page_count needs no backfill
--
-- Unlike preview_status in 000003 or the scan columns in 000005, every row
-- that exists before this migration runs is already correct under the new
-- column: every preview ever produced by this service, before today, is
-- exactly one page (an image, or the single page the old pdfFirstPage-only
-- renderer produced). DEFAULT 1 is not a placeholder here, it is the true
-- historical value for 100% of existing rows, so there is nothing to
-- classify and nothing to UPDATE.
--
-- # Why no lock and no emptiness guard, same reasoning as 000003/000005
--
-- preview_page_count is NOT NULL with a DEFAULT, so an INSERT emitted by a
-- file-service binary that predates this migration stays valid. The new table
-- is purely additive. A file-service that predates this migration keeps
-- working unmodified — it simply never writes a row into
-- attachment_preview_pages, and every attachment it produces a preview for
-- keeps preview_page_count at its true value of 1.

BEGIN;

ALTER TABLE files.attachments
    ADD COLUMN preview_page_count SMALLINT NOT NULL DEFAULT 1;

ALTER TABLE files.attachments
    ADD CONSTRAINT attachments_preview_page_count_check CHECK (preview_page_count >= 1);

-- Pages 2..N of a multi-page preview. Page 1 is deliberately absent — see
-- above — so page_number's own CHECK starts at 2, and a row here can only
-- ever describe an attachment that already has a page 1 on the parent row
-- (enforced by the application, which only ever writes these pages in the
-- same statement that publishes the parent as 'ready').
--
-- No status column, no lease, no attempt counter: this table holds only
-- finished, servable pages (see above). No index beyond the primary key is
-- needed either — every read is `WHERE attachment_id = $1 AND page_number =
-- $2`, which the primary key already serves, and nothing ever scans this
-- table for due work the way the worker scans files.attachments.
CREATE TABLE files.attachment_preview_pages (
    attachment_id    UUID        NOT NULL REFERENCES files.attachments (id) ON DELETE CASCADE,
    page_number      SMALLINT    NOT NULL,
    object_id        UUID        NOT NULL,
    size_bytes       BIGINT      NOT NULL,
    wrapped_dek      BYTEA       NOT NULL,
    kek_key_id       TEXT        NOT NULL,
    envelope_version SMALLINT    NOT NULL,
    dek_wrap_version SMALLINT    NOT NULL,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),

    PRIMARY KEY (attachment_id, page_number),

    -- Every extra page is its own encrypted object with its own identity,
    -- exactly like page 1 (attachments_preview_object_id_unique in 000003):
    -- two rows can never point at the same stored object, so a delete of one
    -- page's object can never strand another's pointer.
    CONSTRAINT attachment_preview_pages_object_id_unique UNIQUE (object_id),

    CONSTRAINT attachment_preview_pages_page_number_check CHECK (page_number >= 2),
    CONSTRAINT attachment_preview_pages_size_check CHECK (size_bytes > 0),
    CONSTRAINT attachment_preview_pages_kek_key_id_length_check CHECK (
        char_length(kek_key_id) BETWEEN 1 AND 64
    ),
    CONSTRAINT attachment_preview_pages_envelope_version_check CHECK (envelope_version > 0),
    -- Pinned to the version this build implements, exactly like the parent
    -- attachment's own columns. A future wrap format needs its own migration.
    CONSTRAINT attachment_preview_pages_dek_wrap_version_check CHECK (dek_wrap_version = 2)
);

COMMIT;
