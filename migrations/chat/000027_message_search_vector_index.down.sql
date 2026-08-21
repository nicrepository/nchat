-- 000027_message_search_vector_index.down.sql
BEGIN;

DROP FUNCTION IF EXISTS chat.message_search_rank(tsvector, tsquery, timestamptz);

DROP INDEX IF EXISTS chat.idx_messages_search_vector;

DROP TRIGGER IF EXISTS channels_search_vector_resync ON chat.channels;
DROP FUNCTION IF EXISTS chat.channel_messages_search_vector_resync();

DROP TRIGGER IF EXISTS messages_search_vector_sync ON chat.messages;
DROP FUNCTION IF EXISTS chat.messages_search_vector_sync();

-- search_vector is entirely derived from body_text + channel type; dropping it
-- loses no original message content.
ALTER TABLE chat.messages
    DROP COLUMN IF EXISTS search_vector;

COMMIT;
