BEGIN;
ALTER TABLE chat.messages DROP CONSTRAINT messages_status_check;
COMMIT;
