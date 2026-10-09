-- Dropping the cursor leaves last_read_at, which #1082's writer only ever set to
-- a boundary its cursor already covered: what remains reads as unread anything
-- the cursor alone had read, and never the reverse.
BEGIN;
ALTER TABLE chat.conversation_read_state
    DROP CONSTRAINT IF EXISTS conversation_read_state_cursor_pair_check,
    DROP COLUMN IF EXISTS cursor_message_id,
    DROP COLUMN IF EXISTS cursor_created_at;
COMMIT;
