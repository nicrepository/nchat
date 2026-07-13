BEGIN;

DROP TABLE IF EXISTS chat.message_edit_history;

ALTER TABLE chat.workspaces
    DROP COLUMN IF EXISTS edit_window_seconds;

ALTER TABLE chat.messages
    DROP COLUMN IF EXISTS edit_count;

-- edited_at belongs to 000004_chat_messages and must survive this rollback.
COMMIT;
