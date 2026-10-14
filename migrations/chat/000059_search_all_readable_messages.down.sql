-- 000059_search_all_readable_messages.down.sql
-- Restores the 000027 public-only index: the channel-aware trigger, the resync
-- trigger, and NULL vectors for everything that is not an active message of an
-- active public channel. search_vector is derived data; nothing is lost.
BEGIN;

DROP FUNCTION IF EXISTS chat.message_search_rank(tsvector, tsquery, timestamptz, timestamptz);

CREATE OR REPLACE FUNCTION chat.messages_search_vector_sync()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
DECLARE
    channel_is_searchable boolean;
BEGIN
    IF NEW.channel_id IS NULL OR NEW.status <> 'active' THEN
        NEW.search_vector := NULL;
        RETURN NEW;
    END IF;

    SELECT (type = 'public' AND status = 'active') INTO channel_is_searchable
    FROM chat.channels
    WHERE id = NEW.channel_id
    FOR SHARE;

    IF COALESCE(channel_is_searchable, false) THEN
        NEW.search_vector := to_tsvector('portuguese', COALESCE(NEW.body_text, ''));
    ELSE
        NEW.search_vector := NULL;
    END IF;

    RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION chat.channel_messages_search_vector_resync()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
BEGIN
    UPDATE chat.messages
    SET search_vector = CASE
            WHEN NEW.type = 'public' AND NEW.status = 'active'
                THEN to_tsvector('portuguese', COALESCE(body_text, ''))
            ELSE NULL
        END
    WHERE channel_id = NEW.id
      AND status = 'active';

    RETURN NULL;
END;
$$;

DROP TRIGGER IF EXISTS channels_search_vector_resync ON chat.channels;
CREATE TRIGGER channels_search_vector_resync
    AFTER UPDATE OF type, status ON chat.channels
    FOR EACH ROW
    WHEN (OLD.type IS DISTINCT FROM NEW.type OR OLD.status IS DISTINCT FROM NEW.status)
    EXECUTE FUNCTION chat.channel_messages_search_vector_resync();

UPDATE chat.messages m
SET search_vector = NULL
WHERE m.search_vector IS NOT NULL
  AND NOT EXISTS (
      SELECT 1 FROM chat.channels c
      WHERE c.id = m.channel_id AND c.type = 'public' AND c.status = 'active'
  );

COMMIT;
