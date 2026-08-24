-- 000036_dashboard_time_window_indexes.up.sql
-- Admin Console observability (issue #581): the index the dashboard's 24 h
-- message counters read.
--
-- No table, no column, no constraint and no data is touched.
--
-- ---------------------------------------------------------------------------
-- Why an index at all
--
-- Two dashboard counters filter chat.messages by created_at across the whole
-- platform: messages in the last 24 hours, and messages withheld by a
-- malicious Link Scan verdict in the same window. Every index migration 000004
-- created leads with workspace_id, so none of them can answer a global time
-- range; without this the two counters are a sequential scan of the largest
-- table in the schema, on every dashboard collection.
--
-- Why BRIN and not B-tree
--
-- chat.messages is append-only and its physical order tracks created_at: rows
-- are inserted in time order and never updated in a way that moves them. That
-- is precisely the correlation BRIN exploits, and it is what makes this index
-- a few kilobytes where the equivalent B-tree would be a substantial fraction
-- of the table. The dashboard reads a wide trailing range rather than a narrow
-- point lookup, so BRIN's block-range granularity costs nothing here: the
-- planner still has to read the matching ranges, and those are the ranges the
-- answer lives in.
--
-- The trade-off, stated plainly: BRIN is useless for the point lookups on this
-- table. It is not intended to serve them — those are already covered by the
-- workspace-leading B-trees, which stay exactly as they are.
-- ---------------------------------------------------------------------------

BEGIN;

SET LOCAL search_path = chat, public;

CREATE INDEX idx_messages_created_at_brin
    ON chat.messages USING brin (created_at);

COMMIT;
