BEGIN;

-- Drops exactly what the up added and nothing else, in the order that leaves a
-- partially-applied database reversible: the table first, because it references
-- chat.messages, then the column on chat.messages itself.
--
-- Dropping the table discards recorded acknowledgements. That is the honest
-- consequence of reverting the feature that created them — there is nowhere
-- else for a per-recipient state to go — and it is why nothing outside this
-- feature is allowed to read this table.
DROP TABLE IF EXISTS chat.message_acknowledgements;

ALTER TABLE chat.messages DROP COLUMN IF EXISTS acknowledgement_required;

COMMIT;
