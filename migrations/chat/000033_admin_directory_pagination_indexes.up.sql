-- 000033_admin_directory_pagination_indexes.up.sql
-- Admin Console management (issue #579): the indexes the two remaining
-- platform-wide directories page on.
--
-- Migration 000032 covered the channel directory. These two tables were left
-- behind because their listings look scoped and are not: the Admin Console
-- reads them across the whole platform, with no workspace in the request, so
-- every index those tables already have leads with the wrong column.
--
-- No table, no column, no constraint and no data is touched.

BEGIN;

SET LOCAL search_path = chat, public;

-- ---------------------------------------------------------------------------
-- Private conversation metadata: GET /api/admin/conversations
--
-- The listing is newest-activity-first and resumes with the row-value
-- comparison (updated_at, id) < (cursor), so the sort key, the resumption
-- predicate and this index are the same two columns in the same order and the
-- same direction.
--
-- Not served by anything that exists. All three indexes from migration 000003
-- lead with workspace_id:
--
--   idx_dm_conversations_workspace       (workspace_id, status, updated_at DESC)
--   idx_dm_conversations_active          (workspace_id, updated_at DESC) WHERE status = 'active'
--   idx_dm_conversations_direct_pair_unique (workspace_id, direct_pair_key) WHERE type = 'direct'
--
-- A B-tree leading with workspace_id cannot produce a global updated_at order:
-- it would have to be walked once per workspace and merged. Those three stay —
-- they serve the workspace-scoped reads chat-service does, which is a different
-- access pattern, not a superseded one.
--
-- Deliberately not partial. workspace_id, type and status are all optional
-- filters on this endpoint, so there is no predicate every query carries, and a
-- partial index would stop covering the listing the moment an operator cleared
-- a filter.
-- ---------------------------------------------------------------------------

CREATE INDEX idx_dm_conversations_directory_page
    ON chat.dm_conversations (updated_at DESC, id DESC);

-- ---------------------------------------------------------------------------
-- Operational policies: GET /api/admin/policies/anti-spam and .../upload
--
-- Both read one projection of chat.workspaces, ordered newest-first and resumed
-- by (created_at, id) < (cursor).
--
-- The only index this table has is idx_workspaces_status (status), which the
-- query never filters on, and the primary key is id alone — so it does not
-- cover the ordering's tiebreak either. Without this the policy screens sort
-- the whole workspace table to return a page.
--
-- Not partial: the policy listings show disabled workspaces too, because an
-- operator needs to see the limit a disabled workspace still carries.
-- ---------------------------------------------------------------------------

CREATE INDEX idx_workspaces_directory_page
    ON chat.workspaces (created_at DESC, id DESC);

COMMIT;
