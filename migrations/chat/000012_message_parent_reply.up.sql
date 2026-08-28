BEGIN;

-- parent_message_id, its original FK, and idx_messages_parent come from 000004.
-- This migration only upgrades the FK delete behavior, while staying replay-safe
-- for older databases missing the column.
ALTER TABLE chat.messages
    ADD COLUMN IF NOT EXISTS parent_message_id UUID;

ALTER TABLE chat.messages
    DROP CONSTRAINT IF EXISTS messages_parent_message_id_fkey;

ALTER TABLE chat.messages
    ADD CONSTRAINT messages_parent_message_id_fkey
    FOREIGN KEY (parent_message_id) REFERENCES chat.messages (id) ON DELETE SET NULL;

COMMIT;
