-- 000018_workspace_message_rate_limit.down.sql
-- Rolling back drops the per-workspace policy and returns enforcement to the
-- compiled-in default (60/min), which is what the column was seeded with.
-- The CHECK constraint is dropped with the column it constrains.

BEGIN;

ALTER TABLE chat.workspaces
    DROP COLUMN IF EXISTS message_rate_limit_per_minute;

COMMIT;
