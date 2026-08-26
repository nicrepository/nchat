BEGIN;
ALTER TABLE chat.messages RENAME TO posts;
COMMIT;
