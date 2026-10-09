-- Issue #1082: a precise read cursor, stored beside the legacy boundary.
--
-- last_read_at / last_read_message_id keep their pre-#1082 meaning, exactly and
-- forever: everything created at or before last_read_at is read, and the
-- message id is informational. A release that predates this migration reads
-- and writes only those two columns, and nothing it does can touch the cursor.
--
-- cursor_created_at / cursor_message_id are one message's (created_at, id)
-- position in the timeline's canonical order: that message and everything
-- before it are read. Only #1082's code writes them, and it only ever moves
-- them forward.
--
-- A message is read when either says so. Both are prefixes of the same total
-- order, so the union is simply the later of the two, and it only grows.
-- #1082's writer also raises last_read_at to just below the cursor's instant —
-- every message strictly older than it is covered by the cursor — so a previous
-- release, should traffic roll back to it, reads a subset of what was read and
-- never more.
--
-- Blue/Green: additive only; no existing column changes meaning.
BEGIN;

ALTER TABLE chat.conversation_read_state
    ADD COLUMN cursor_created_at TIMESTAMPTZ,
    ADD COLUMN cursor_message_id UUID,
    ADD CONSTRAINT conversation_read_state_cursor_pair_check
        CHECK ((cursor_created_at IS NULL) = (cursor_message_id IS NULL));

COMMIT;
