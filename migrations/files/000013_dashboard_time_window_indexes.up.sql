-- 000013_dashboard_time_window_indexes.up.sql
-- Admin Console observability (issue #581): the index the dashboard's 24 h
-- attachment counters read.
--
-- No table, no column, no constraint and no data is touched. The rationale for
-- choosing BRIN is the same as in chat migration 000036: the table is
-- append-only, its physical order tracks created_at, and the dashboard reads a
-- wide trailing range rather than a point lookup.
--
-- Two counters, two columns:
--
--   * uploads in the last 24 hours filters on created_at;
--   * files the antimalware rejected in the last 24 hours filters on
--     updated_at, because the moment that matters for that counter is when the
--     verdict landed, not when the upload started.
--
-- updated_at is written when a row reaches a terminal state, so its correlation
-- with physical order is weaker than created_at's — a row can be updated long
-- after insertion. It is still strongly correlated in practice, because the
-- scan pipeline settles a row within minutes of its upload, and a BRIN summary
-- that is merely good is worth its few kilobytes against a sequential scan.
--
-- The existing B-trees (idx_attachments_channel, idx_attachments_conversation,
-- idx_attachments_pending) all lead with a column the dashboard does not
-- filter on, so none of them can serve either counter. They stay as they are.

BEGIN;

SET LOCAL search_path = files, public;

CREATE INDEX idx_attachments_created_at_brin
    ON files.attachments USING brin (created_at);

CREATE INDEX idx_attachments_updated_at_brin
    ON files.attachments USING brin (updated_at);

COMMIT;
