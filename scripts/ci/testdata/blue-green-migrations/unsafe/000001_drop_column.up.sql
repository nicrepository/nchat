BEGIN;
ALTER TABLE chat.messages DROP COLUMN legacy_body;
COMMIT;
