BEGIN;

-- Drops exactly what the up added and nothing else. The constraints go first:
-- dropping the columns would take them along, but naming them keeps this
-- readable against a database where a partial apply left one without the other.
ALTER TABLE chat.channels DROP CONSTRAINT IF EXISTS channels_description_length_check;
ALTER TABLE chat.dm_conversations DROP CONSTRAINT IF EXISTS dm_conversations_description_length_check;

ALTER TABLE chat.channels DROP COLUMN IF EXISTS description;
ALTER TABLE chat.dm_conversations DROP COLUMN IF EXISTS description;

COMMIT;
