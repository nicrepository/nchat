BEGIN;
ALTER TABLE chat.messages ADD COLUMN edit_count INTEGER DEFAULT 0;
UPDATE chat.messages SET edit_count = 0 WHERE edit_count IS NULL;
COMMIT;
