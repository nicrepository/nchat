-- 000027_message_search_vector_index.up.sql
-- RF-15 (issue #123, TASK-94): PostgreSQL full-text search infrastructure for
-- chat messages — tsvector, GIN index, and a relevance/recency ranking helper.
--
-- Design decisions:
--   - search_vector is a plain (non-generated) tsvector column, maintained by
--     a BEFORE INSERT OR UPDATE trigger on chat.messages. A stored generated
--     column was rejected: it can only read columns on its own row, and
--     whether a message is searchable depends on chat.channels.type, a
--     different table entirely (chat.messages has no column of its own that
--     says "my channel is public").
--   - search_vector stays NULL for everything that must never be searchable:
--     DMs (channel_id IS NULL), messages in private or archived channels, and
--     soft-deleted messages (status = 'deleted'; see 000004 — no hard DELETE).
--     An archived channel is excluded for the same reason a private one is:
--     chat-service already treats status = 'archived' as unavailable in
--     ordinary read flows (ArchiveChannel), so the search index follows that
--     semantics rather than inventing its own. NULL, not an empty tsvector,
--     so the partial GIN index below can only ever contain public, active,
--     channel content.
--   - chat.channels.type and chat.channels.status are both mutable:
--     chat-service's channel update flow (PGXChannelStore.updateChannel)
--     flips public <-> private, and ArchiveChannel flips active -> archived.
--     A trigger scoped to chat.messages alone cannot observe either change,
--     so a second trigger on chat.channels resyncs every message of the
--     channel whenever type or status actually changes. This is the accepted
--     write amplification: it only runs on those changes, infrequent admin
--     actions, never on ordinary message traffic.
--   - Race with concurrent channel privacy/archive changes: the message
--     trigger below reads chat.channels with SELECT ... FOR SHARE rather than
--     a plain SELECT. Without it, a message INSERT that reads "channel is
--     public" and a concurrent UPDATE that flips the channel to private or
--     archived could interleave so that whichever commits second does not
--     observe the other, leaving a non-null search_vector behind a channel
--     that is not public and active. FOR SHARE conflicts with the row lock an
--     UPDATE takes, so one of the two transactions always blocks until the
--     other commits and is then evaluated against its final, committed state.
--     This is the weakest lock that provides that guarantee: it never
--     conflicts with another FOR SHARE/FOR KEY SHARE reader, only with a
--     concurrent writer of the same channel row.
--   - 'portuguese' is hardcoded in both the sync trigger and the ranking
--     helper, never left to the database/search_path default.
--   - No duplicate text column: search_vector is a tsvector derived from
--     body_text, not a copy of it.

BEGIN;

ALTER TABLE chat.messages
    ADD COLUMN search_vector tsvector;

-- Keeps search_vector correct on every insert and on every edit or soft
-- delete of a channel message. Restricted to (body_text, status) so ordinary
-- updates that touch neither (e.g. edited_at-only bookkeeping elsewhere,
-- reactions, pins) never re-run it, and so the bulk resync below — which
-- writes only search_vector — never re-triggers itself.
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

    -- FOR SHARE: serializes against a concurrent UPDATE of this channel's
    -- type/status (see the design note above). Blocks until any such
    -- in-flight change commits or rolls back, then reads its final value.
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

CREATE TRIGGER messages_search_vector_sync
    BEFORE INSERT OR UPDATE OF body_text, status ON chat.messages
    FOR EACH ROW
    EXECUTE FUNCTION chat.messages_search_vector_sync();

-- Fires only when a channel's type or status actually changes (the WHEN
-- clause), and resyncs every active message of that channel in one
-- statement: leaving public+active clears search_vector for all of them
-- (whether the channel went private or archived), entering public+active
-- populates it (whether from private or from archived). This is what stops a
-- message from staying searchable after its channel goes private or
-- archived, and what makes a message searchable again once its channel
-- returns to public+active. This UPDATE runs as part of the same
-- transaction that changed the channel row, so it always sees the channel's
-- own new value and needs no FOR SHARE of its own; the row lock the
-- triggering UPDATE already holds is what the message trigger's FOR SHARE
-- above serializes against.
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

CREATE TRIGGER channels_search_vector_resync
    AFTER UPDATE OF type, status ON chat.channels
    FOR EACH ROW
    WHEN (OLD.type IS DISTINCT FROM NEW.type OR OLD.status IS DISTINCT FROM NEW.status)
    EXECUTE FUNCTION chat.channel_messages_search_vector_resync();

-- Backfill: rows inserted before this migration predate the trigger.
UPDATE chat.messages m
SET search_vector = to_tsvector('portuguese', COALESCE(m.body_text, ''))
FROM chat.channels c
WHERE c.id = m.channel_id
  AND c.type = 'public'
  AND c.status = 'active'
  AND m.status = 'active';

-- Partial GIN: only rows that are actually searchable ever enter the index.
-- This is what keeps DMs, private-channel messages, and deleted messages out
-- of the index structurally, not just by trigger convention, and keeps the
-- index itself small.
CREATE INDEX idx_messages_search_vector
    ON chat.messages USING GIN (search_vector)
    WHERE search_vector IS NOT NULL;

-- Ranking helper: relevance (ts_rank) is the primary component. Recency is a
-- bounded multiplicative boost — at most +10%, for messages from the last 30
-- days, decaying linearly to 0 by day 30. The bound is exact: two results
-- within 10% of each other's ts_rank can have their order flipped by the
-- boost, but a material relevance difference (more than ~10% apart) cannot
-- be — the older, more relevant result still wins in that case.
CREATE OR REPLACE FUNCTION chat.message_search_rank(
    p_search_vector tsvector,
    p_query tsquery,
    p_created_at timestamptz
) RETURNS double precision
LANGUAGE sql
STABLE
PARALLEL SAFE
AS $$
    SELECT ts_rank(p_search_vector, p_query)
         * (1.0 + 0.1 * GREATEST(0.0, LEAST(1.0,
             (30.0 - EXTRACT(EPOCH FROM (now() - p_created_at)) / 86400.0) / 30.0
           )));
$$;

COMMIT;
