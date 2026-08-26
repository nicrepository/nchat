BEGIN;
ALTER TABLE chat.messages RENAME COLUMN body TO content;
COMMIT;
