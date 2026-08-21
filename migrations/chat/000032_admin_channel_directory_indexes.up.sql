-- 000032_admin_channel_directory_indexes.up.sql
-- Admin Console management (issue #579): the index the platform channel
-- directory pages on.
--
-- No table and no column is added. chat.channels already carries everything the
-- console lists; what was missing is an ordering the database can serve.
--
-- Why (created_at DESC, id DESC):
--   the directory is newest-first and pages by keyset on exactly this pair, so
--   the sort and the resumption predicate are answered by one range scan. id is
--   the tiebreak because created_at is not unique, and a keyset without a
--   unique trailing column can repeat or skip rows between pages.
--
--   The existing idx_channels_workspace_status leads with workspace_id, so it
--   cannot serve the unfiltered, platform-wide ordering this directory starts
--   from. It still serves the workspace-filtered variant, and is left alone.
--
-- Lock note: CREATE INDEX takes a SHARE lock on chat.channels for the duration
-- of the build. CONCURRENTLY cannot run inside the transaction this
-- repository's migration runner requires; the table holds one row per channel,
-- so the build is brief. No table is rewritten.

BEGIN;

SET LOCAL search_path = chat, public;

CREATE INDEX idx_channels_directory_page
    ON chat.channels (created_at DESC, id DESC);

COMMIT;
