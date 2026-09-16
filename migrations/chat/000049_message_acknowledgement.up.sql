BEGIN;

-- Recipient acknowledgement (issue #824, parent #820).
--
-- Two facts, deliberately separate.
--
-- 1. acknowledgement_required on chat.messages: the author asked for explicit
--    confirmation. Like priority in 000047 it is the author's own claim about
--    their own message, it authorises nothing, and no predicate anywhere reads
--    it to decide access.
--
-- 2. chat.message_acknowledgements: one row per eligible recipient, carrying
--    that recipient's own state. This is the fact the product is actually
--    about — "did *this person* confirm" — and it cannot live on the message,
--    because a group message has one row and many answers.
--
-- What this table is NOT: it is not read state, not delivery state, and not an
-- unread counter. chat.conversation_read_state already holds when somebody last
-- read a conversation, and nothing here reads it or is written by it. Opening a
-- message produces no row change in this table at all — the acknowledgement is
-- the explicit act of pressing a button, and #820 states the separation as
-- DELIVERED != READ != ACKNOWLEDGED. Folding the two together would be the one
-- mistake that cannot be undone later: once "read" has silently meant
-- "acknowledged" for a while there is no way to tell the two apart in history.

-- NOT NULL DEFAULT false rather than nullable, for the same reason 000047's
-- priority is NOT NULL DEFAULT 'standard': "no answer" is not a state this
-- product has. Every message written before this column existed asked for no
-- acknowledgement, and saying so in the schema is what saves every reader from
-- a COALESCE it could forget.
--
-- No table rewrite: PostgreSQL 11+ records an ADD COLUMN ... DEFAULT in the
-- catalogue and materialises it on the next write of each row, so this is a
-- metadata change on a table that grows with every message ever sent. The
-- ACCESS EXCLUSIVE lock is held for that catalogue write only.
--
-- BOOLEAN rather than TEXT with a CHECK — the contrast with priority is the
-- point. priority is TEXT because its vocabulary is open to a fourth value one
-- day; this axis is closed by construction and boolean already enforces it, so
-- there is no constraint to add and no two-step validation to perform.
ALTER TABLE chat.messages
    ADD COLUMN acknowledgement_required BOOLEAN NOT NULL DEFAULT false;

-- One row per (message, recipient), written when the message is created and
-- only for a message that asked for acknowledgement.
--
-- The recipient set is a snapshot taken at send time, not a view over current
-- membership. "Who was asked" is a historical fact: somebody who joins the
-- channel tomorrow was never asked, and somebody who leaves was still asked and
-- their answer still counts. Deriving the list from live membership on every
-- read would silently rewrite that history, and would make the sender's
-- "4 of 7 confirmed" mean a different thing every time it is rendered.
--
-- The recipients themselves are derived in the database, inside the same
-- statement that inserts the message (see createMessageQuery). Nothing about
-- them comes from the request: a client never names a recipient, so a forged
-- recipient_id is not a request a client can make.
CREATE TABLE chat.message_acknowledgements (
    message_id   UUID NOT NULL REFERENCES chat.messages (id) ON DELETE CASCADE,
    recipient_id UUID NOT NULL REFERENCES auth.users (id) ON DELETE CASCADE,

    -- The per-recipient state machine of #820. PENDING is the only unresolved
    -- state; the other four are terminal and none of them returns to it.
    --
    --   pending ─ acknowledge ─> acknowledged
    --           ─ reply ───────> responded
    --           ─ expiry ──────> expired
    --           ─ cancel ──────> cancelled
    --
    -- Every transition is applied as a single UPDATE ... WHERE state='pending',
    -- so two concurrent resolutions cannot both win and the loser changes
    -- nothing. `expired` has no producer yet: issue #825 owns the reminder
    -- lifecycle that decides when an unanswered request stops being asked, and
    -- declaring the value here is what lets that worker be written without a
    -- migration that widens a constraint under load.
    state        TEXT NOT NULL DEFAULT 'pending',

    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- When this recipient's request stopped being pending, whatever ended it.
    -- It is the acknowledged_at #824 asks for, generalised to the four ways a
    -- request can be resolved rather than the one — a column that could only
    -- record acknowledgement would leave a responded row with no instant at all.
    resolved_at  TIMESTAMPTZ,

    -- The logical uniqueness #824 requires, expressed as identity rather than as
    -- a separate unique index: a recipient *is* their row. Two acknowledgements
    -- from the same person for the same message cannot become two rows, whether
    -- they arrive as a double click, an HTTP retry or two concurrent requests on
    -- two connections, and the application is not what is preventing it.
    --
    -- It is also the access path for every query this feature has: the
    -- acknowledge CAS reads one full key, and the summary and the sender's
    -- detail list read the message_id prefix. No further index is created,
    -- because no query exists that would use one.
    PRIMARY KEY (message_id, recipient_id),

    CONSTRAINT message_acknowledgements_state_check
        CHECK (state IN ('pending', 'acknowledged', 'responded', 'expired', 'cancelled')),

    -- Resolution and its instant are one fact, so the schema refuses to hold
    -- half of it: a pending row with a resolved_at, or a terminal row without
    -- one, are both states no code path should be able to write. The domain says
    -- the same thing; this is what makes it true for a repair script as well.
    CONSTRAINT message_acknowledgements_resolved_at_check
        CHECK ((state = 'pending') = (resolved_at IS NULL))
);

COMMIT;
