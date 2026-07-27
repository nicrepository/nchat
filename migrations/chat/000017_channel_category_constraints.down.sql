-- 000017_channel_category_constraints.down.sql
-- Removes only what the up migration added and restores the index it replaced.
--
-- No row was inserted, updated or deleted on the way up, so there is no data to
-- restore. In particular no channel and no channel category is dropped here:
-- rolling back the bounds must never cost a row.

BEGIN;

DROP INDEX IF EXISTS chat.idx_channels_workspace_category;

-- Restore the workspace-only index from 000001 before dropping the composite
-- one that superseded it, so the table is never left without an index on
-- workspace_id.
CREATE INDEX IF NOT EXISTS idx_channel_categories_workspace
    ON chat.channel_categories (workspace_id);

DROP INDEX IF EXISTS chat.channel_categories_workspace_position_idx;
DROP INDEX IF EXISTS chat.channel_categories_workspace_name_uidx;

ALTER TABLE chat.channel_categories
    DROP CONSTRAINT IF EXISTS channel_categories_position_range_check;

ALTER TABLE chat.channel_categories
    DROP CONSTRAINT IF EXISTS channel_categories_name_not_reserved_check;

ALTER TABLE chat.channel_categories
    DROP CONSTRAINT IF EXISTS channel_categories_name_no_control_check;

ALTER TABLE chat.channel_categories
    DROP CONSTRAINT IF EXISTS channel_categories_name_trimmed_check;

ALTER TABLE chat.channel_categories
    DROP CONSTRAINT IF EXISTS channel_categories_name_length_check;

COMMIT;
