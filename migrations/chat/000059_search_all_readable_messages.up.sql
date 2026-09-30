-- 000059_search_all_readable_messages.up.sql
-- Issue #900: global search finds messages in every conversation the caller
-- may read — public channels, private channels they belong to, and their own
-- direct and group conversations — not only public channels.
--
-- Design decisions:
--   - Authorization moves entirely to query time. 000027 kept private, archived
--     and DM content out of the index so a public-only query could not reach
--     it; with members now entitled to their private content, the index can no
--     longer be the boundary. search-service reads every match through the
--     same visibility predicates chat-service applies to reads
--     (chat.channel_visible_to_user for channels, an active dm_members row for
--     conversations) and drops anything else before ranking.
--   - The vector therefore depends on the message row alone: active messages
--     are indexed, everything else is NULL. No chat.channels lookup remains, so
--     the FOR SHARE read and the channel resync trigger 000027 needed to follow
--     type/status flips are gone — a flip changes who may read, which the query
--     already decides, not what is indexed.
--   - Blue/Green: a slot still running the previous search-service joins
--     chat.channels on type = 'public' and never reaches DM rows (their
--     channel_id is NULL), so the extra vectors are invisible to it. This
--     migration only expands what is indexed.
--   - The backfill is one UPDATE, the same shape 000027 used, and only touches
--     active rows still NULL (DM, private and archived-channel messages).
--   - message_search_rank gains an overload taking the reference time. The
--     3-argument form reads now(), so every page of a search ranked the same
--     rows against a later clock and a (score, created_at, id) cursor stopped
--     lining up: rows repeated or were skipped. The cursor now carries the
--     time the first page was ranked at and every page reuses it. Same
--     formula; the 3-argument form stays for anything already calling it.

BEGIN;

CREATE OR REPLACE FUNCTION chat.messages_search_vector_sync()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
BEGIN
    IF NEW.status = 'active' THEN
        NEW.search_vector := to_tsvector('portuguese', COALESCE(NEW.body_text, ''));
    ELSE
        NEW.search_vector := NULL;
    END IF;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS channels_search_vector_resync ON chat.channels;
DROP FUNCTION IF EXISTS chat.channel_messages_search_vector_resync();

CREATE OR REPLACE FUNCTION chat.message_search_rank(
    p_search_vector tsvector,
    p_query tsquery,
    p_created_at timestamptz,
    p_ranked_at timestamptz
) RETURNS double precision
LANGUAGE sql
STABLE
PARALLEL SAFE
AS $$
    SELECT ts_rank(p_search_vector, p_query)
         * (1.0 + 0.1 * GREATEST(0.0, LEAST(1.0,
             (30.0 - EXTRACT(EPOCH FROM (p_ranked_at - p_created_at)) / 86400.0) / 30.0
           )));
$$;

UPDATE chat.messages
SET search_vector = to_tsvector('portuguese', COALESCE(body_text, ''))
WHERE status = 'active'
  AND search_vector IS NULL;

COMMIT;
