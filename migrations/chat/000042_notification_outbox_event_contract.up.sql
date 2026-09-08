-- 000042_notification_outbox_event_contract.up.sql
-- Issue #741 (parent #678, RNF-25): turns the mention-only outbox created by
-- 000006 into the durable notification event contract the worker and the policy
-- engine will be built on.
--
-- # Why this table and not a new one
--
-- chat.notification_outbox already exists, and it is already written by the same
-- PostgreSQL statement that inserts the message (CreateMessage) and by the one
-- that promotes a link-withheld message (ResolveDecidedMessages). That shared
-- boundary is the entire point of a transactional outbox and it is already
-- correct here: there is no commit in which a message exists without its
-- notification rows, and none in which a rolled back message leaves them behind.
--
-- A second table in a notifications schema would have to reproduce that boundary
-- and would leave two tables answering the same question, which is the one thing
-- issue #741 forbids outright. The notification-service worker will read this
-- table across schemas exactly as its SMTP worker already reads
-- auth.email_outbox — one database, one source of truth.
--
-- # What is deliberately NOT here
--
-- No message body, no push subscription, no token, no delivery payload. The row
-- carries references only: which workspace, which recipient, which event, which
-- source entity. Everything a delivery needs to render is read back later
-- through the same authorization every other read path applies, so a leaked
-- outbox row grants nothing the reader did not already have.
--
-- No attempt counter, no lease, no next_retry_at. Those belong to the worker
-- that defines the claim protocol, and inventing them now would fix a retry
-- design nothing has exercised.
--
-- # Retention (documented, not implemented)
--
-- Terminal rows — sent, suppressed, failed — are the only ones that accumulate,
-- and nothing reads them after the fact: the delivery record lives with the
-- delivery channel. The intended policy is a periodic delete of terminal rows
-- older than 30 days, driven by the notification-service worker once it exists,
-- bounded per pass so it never takes a long lock. idx_notification_outbox_open
-- is partial over the non-terminal states, so it does not grow with the rows a
-- retention job would remove.

-- nchat:blue-green contract-phase every removal here is replaced in the same
-- transaction by something strictly broader, so a slot still running the
-- previous release keeps working. The two DROP CONSTRAINTs are widenings: kind
-- goes from {mention} to a superset, status from four states to a superset of
-- the same four, so every value the old code writes still passes and every value
-- it reads still exists. The DROP INDEX removes a partial index over
-- status='pending' whose rows are all covered by the broader partial index
-- created below; no query plan the old slot depends on loses its index, and
-- nothing in either release reads that index by name. The legacy UNIQUE
-- constraint is deliberately NOT dropped: the previous release's INSERT names it
-- in ON CONFLICT, so removing it now would break that slot outright. It is
-- retired in a later contract release, before any reaction producer is
-- connected.

BEGIN;

-- ---------------------------------------------------------------------------
-- Event type: the outbox stops being mention-only
-- ---------------------------------------------------------------------------
--
-- The set is wider than what produces rows today, on purpose. Connected by this
-- issue: mention, reply, direct_message. Declared and not produced:
-- channel_message, reaction, call. A vocabulary that needs a migration every
-- time a producer is connected is a vocabulary producers work around.
ALTER TABLE chat.notification_outbox
    DROP CONSTRAINT notification_outbox_kind_check;

ALTER TABLE chat.notification_outbox
    ADD CONSTRAINT notification_outbox_kind_check
    CHECK (kind IN (
        'direct_message', 'mention', 'reply', 'channel_message', 'reaction', 'call'
    )) NOT VALID;

-- ---------------------------------------------------------------------------
-- State: suppressed is not failed, and failed is not sent
-- ---------------------------------------------------------------------------
--
-- The four original states could not express the distinction this issue exists
-- to guarantee. "Nobody was told, on purpose", "somebody was told" and "we tried
-- and could not" are three different outcomes, and an outbox that renders the
-- first as the third turns a working quiet-hours rule into a delivery incident.
--
--   pending    a producer wrote it; no policy has looked at it yet
--   eligible   a policy decided it should be delivered
--   suppressed a policy decided it should not be  (terminal)
--   processing a worker claimed it and is sending
--   sent       a channel accepted it              (terminal)
--   retrying   delivery failed, another attempt is due
--   failed     delivery failed permanently        (terminal)
--
-- The issue's `evaluated` step is a transition, not a resting place: a row
-- leaves pending as eligible or as suppressed and is never observed mid
-- decision. A state nothing can observe would only be a value the worker has to
-- handle and can never see.
ALTER TABLE chat.notification_outbox
    DROP CONSTRAINT notification_outbox_status_check;

ALTER TABLE chat.notification_outbox
    ADD CONSTRAINT notification_outbox_status_check
    CHECK (status IN (
        'pending', 'eligible', 'suppressed', 'processing', 'sent', 'retrying', 'failed'
    )) NOT VALID;

-- ---------------------------------------------------------------------------
-- The event's own facts
-- ---------------------------------------------------------------------------
--
-- source_type is what stops a reaction id from ever colliding with a message id
-- that happens to be equal. It defaults to 'message' because every row that
-- exists today has one, and message_id keeps its NOT NULL for the same reason:
-- relaxing it now, for producers that do not exist yet, would weaken the FK that
-- currently guarantees an outbox row disappears with the message it names.
--
-- occurred_at is when the thing happened, which is not created_at once an import
-- exists. Backfilled below from created_at, which is exactly right for every row
-- written so far.
--
-- origin is the historical/import/replay marker. It is a fact about provenance
-- and nothing more: no rule here says what a policy should do with it. A
-- timestamp cannot substitute for it — an import writes old occurred_at values
-- and a replay writes new ones — so the producer states it outright.
--
-- suppressed_reason is free text bounded by a length, not an enum: the reasons
-- belong to the policy engine that has not been written, and guessing them now
-- would produce a vocabulary it has to migrate away from. The bound is real and
-- enforced below — 200 characters, the same one dedupe_key uses. Unbounded, this
-- column would be the one place in the table a caller could park arbitrary text,
-- which is exactly the content the outbox is designed not to carry.
ALTER TABLE chat.notification_outbox
    ADD COLUMN source_type       TEXT        NOT NULL DEFAULT 'message',
    ADD COLUMN occurred_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    ADD COLUMN priority          TEXT        NOT NULL DEFAULT 'normal',
    ADD COLUMN origin            TEXT        NOT NULL DEFAULT 'live',
    ADD COLUMN suppressed_reason TEXT,
    ADD COLUMN dedupe_key        TEXT,
    ADD COLUMN updated_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    ADD COLUMN processed_at      TIMESTAMPTZ;

-- Every row written so far is a live mention, created and occurring at the same
-- instant, and its dedupe identity is the one the new unique index expresses.
-- Backfilling it here is what lets the new index be created without excluding
-- the rows that predate it.
UPDATE chat.notification_outbox
   SET occurred_at = created_at,
       updated_at  = created_at,
       dedupe_key  = 'message:' || message_id::text || ':' || kind
 WHERE dedupe_key IS NULL;

-- ---------------------------------------------------------------------------
-- States the domain does not produce
-- ---------------------------------------------------------------------------
--
-- All NOT VALID, and 000043 performs the scan. ADD CONSTRAINT with validation
-- reads the whole table under ACCESS EXCLUSIVE, and this one grows with every
-- notifiable event ever produced. A NOT VALID constraint still checks every
-- insert and update from the instant it commits, so there is no window in which
-- the new columns are unconstrained — only the historical rows go unread, and
-- the UPDATE above has just written all of them.
--
-- The suppressed_reason rule is the one that carries weight: a reason without
-- the state, or the state without a reason, are both rows nobody could interpret.
-- Writing it as an equality of two booleans makes it one constraint instead of
-- two, and makes the intent read as what it is — the two facts arrive together
-- or not at all. A reason on a delivered or failed notification is refused by
-- the same equality: it would claim a suppression that never happened, which is
-- the confusion the three distinct terminal states exist to prevent.
--
-- The length half is in the same constraint because it is the same contract, and
-- libs/go/platform/notificationevent.SuppressedReasonMaxLen is the Go side of it.
ALTER TABLE chat.notification_outbox
    ADD CONSTRAINT notification_outbox_source_type_check
        CHECK (source_type IN ('message', 'reaction', 'call')) NOT VALID,
    ADD CONSTRAINT notification_outbox_priority_check
        CHECK (priority IN ('high', 'normal', 'low')) NOT VALID,
    ADD CONSTRAINT notification_outbox_origin_check
        CHECK (origin IN ('live', 'import', 'replay', 'resync')) NOT VALID,
    ADD CONSTRAINT notification_outbox_suppressed_reason_check
        CHECK (
            (status = 'suppressed') = (suppressed_reason IS NOT NULL)
            AND (suppressed_reason IS NULL OR char_length(suppressed_reason) BETWEEN 1 AND 200)
        ) NOT VALID,
    ADD CONSTRAINT notification_outbox_dedupe_key_check
        CHECK (dedupe_key IS NULL OR char_length(dedupe_key) BETWEEN 1 AND 200) NOT VALID;

-- ---------------------------------------------------------------------------
-- Idempotency: the database is the authority, not a preceding SELECT
-- ---------------------------------------------------------------------------
--
-- dedupe_key is '<source_type>:<source_id>:<event_type>[:<discriminator>]', and
-- libs/go/platform/notificationevent is the authority for that format. The
-- discriminator is what keeps the key from being too broad: two different
-- reactions on one message share the message id, so the actor and the emoji are
-- what make them two events rather than one that silently swallowed the other.
--
-- Uniqueness is qualified by workspace_id and recipient_user_id, which is what
-- makes a collision between two tenants impossible however the key is composed:
-- two workspaces cannot share a row even if they somehow produced identical
-- source ids.
--
-- Partial over NOT NULL because the previous release's INSERT does not write the
-- column. Those rows keep being deduplicated by the legacy UNIQUE constraint,
-- which expresses the same grain for message-sourced events and is retained for
-- exactly this window.
CREATE UNIQUE INDEX notification_outbox_dedupe_uq
    ON chat.notification_outbox (workspace_id, recipient_user_id, dedupe_key)
    WHERE dedupe_key IS NOT NULL;

-- ---------------------------------------------------------------------------
-- Worker consumption
-- ---------------------------------------------------------------------------
--
-- One index, not two. 000006's idx_notification_outbox_pending covered exactly
-- one of the three non-terminal states, so a worker draining the queue would
-- have missed eligible and retrying rows or needed a second index for them. This
-- replaces it with the same shape over all three, so the claim query a worker
-- must run — oldest non-terminal work first — is supported by one partial index
-- whose size is the backlog rather than the history.
--
-- Ordered by occurred_at rather than created_at: an imported batch must not
-- overtake live events simply because it was written later, and the tie-break on
-- id keeps the order total.
DROP INDEX chat.idx_notification_outbox_pending;

CREATE INDEX idx_notification_outbox_open
    ON chat.notification_outbox (occurred_at, id)
    WHERE status IN ('pending', 'eligible', 'retrying');

-- ---------------------------------------------------------------------------
-- The parking table follows the same contract
-- ---------------------------------------------------------------------------
--
-- chat.message_pending_mentions holds the notifications a link-withheld message
-- may not produce yet, and 000024 released them into the outbox as literal
-- 'mention' rows. Now that a message produces reply and direct_message events
-- too, the parked row has to remember which kind it was and how loudly it asked,
-- or a promoted message would announce every one of its recipients as a mention.
--
-- The name stays. Renaming it would break the slot running the previous release
-- for no gain, and what it means is written here.
--
-- No CHECK constraints: this is a staging table whose values are written by the
-- same statement that writes the outbox and are re-checked by the outbox's own
-- constraints on release. A second copy of the vocabulary here would be a second
-- thing to migrate whenever the first one changes.
ALTER TABLE chat.message_pending_mentions
    ADD COLUMN kind     TEXT NOT NULL DEFAULT 'mention',
    ADD COLUMN priority TEXT NOT NULL DEFAULT 'high';

-- ---------------------------------------------------------------------------
-- The state machine, enforced by the database
-- ---------------------------------------------------------------------------
--
-- notification_outbox_status_check says which states exist. It cannot say which
-- ones may follow which, because a CHECK constraint sees only the row it is
-- given and the rule is about the row it replaces. So the transition table is a
-- trigger, and it is the reason a terminal state is actually terminal: a
-- suppressed notification cannot be turned into a delivered one by anything —
-- not a future worker with a bug, not a repair script, not a psql session.
--
-- Without it the three terminal states are a naming convention. "Nobody was
-- told, on purpose", "somebody was told" and "we tried and could not" would be
-- three labels a single UPDATE could rewrite into each other, and every audit
-- built on them would be answering from data nothing defends.
--
-- This mirrors stateTransitions in libs/go/platform/notificationevent, which is
-- the Go authority the storage layer validates against before it writes.
-- TestNotificationOutboxStateMachinePostgreSQL drives every pair through both,
-- so the two cannot drift without a test failing.
--
-- Terminal states are absent from the CASE on purpose: they match no branch, so
-- every transition out of them falls through to the refusal.
--
-- Blue/Green: the previous release has no writer for this column at all — no
-- worker exists yet, and both producers only INSERT — so no slot can be running
-- code this trigger would start refusing.
CREATE FUNCTION chat.enforce_notification_outbox_transition()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
BEGIN
    -- An UPDATE that assigns the same state is not a transition. Refusing it
    -- would make a plain "touch every row" repair impossible for no gain.
    IF NEW.status = OLD.status THEN
        RETURN NEW;
    END IF;

    IF NOT (
        (OLD.status = 'pending'    AND NEW.status IN ('eligible', 'suppressed'))
        OR (OLD.status = 'eligible'   AND NEW.status IN ('processing', 'suppressed'))
        OR (OLD.status = 'processing' AND NEW.status IN ('sent', 'retrying', 'failed'))
        OR (OLD.status = 'retrying'   AND NEW.status IN ('processing', 'failed'))
    ) THEN
        RAISE EXCEPTION 'notification outbox transition % -> % is not allowed',
                        OLD.status, NEW.status
            USING ERRCODE = '23514',
                  CONSTRAINT = 'notification_outbox_transition';
    END IF;

    RETURN NEW;
END;
$$;

CREATE TRIGGER notification_outbox_enforce_transition
    BEFORE UPDATE OF status ON chat.notification_outbox
    FOR EACH ROW
    EXECUTE FUNCTION chat.enforce_notification_outbox_transition();

COMMIT;
