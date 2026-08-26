-- nchat:blue-green contract-phase
BEGIN;
ALTER TABLE chat.messages DROP COLUMN other_body;
COMMIT;
