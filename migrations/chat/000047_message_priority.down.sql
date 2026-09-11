BEGIN;

-- Drops exactly what the up added and nothing else. The constraint goes first:
-- dropping the column would take it with it, but naming it keeps this readable
-- against a database where a partial apply left one without the other.
ALTER TABLE chat.messages DROP CONSTRAINT IF EXISTS messages_priority_check;

ALTER TABLE chat.messages DROP COLUMN IF EXISTS priority;

COMMIT;
