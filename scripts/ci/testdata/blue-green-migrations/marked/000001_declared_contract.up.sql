-- nchat:blue-green contract-phase the expand release shipped two releases ago;
-- no slot still writes chat.messages.legacy_body.
BEGIN;
ALTER TABLE chat.messages DROP COLUMN legacy_body;
COMMIT;
