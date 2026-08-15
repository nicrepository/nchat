-- 000024_message_link_safety.up.sql
-- RF-21 (issue #135): hold a message carrying an unscanned link until Cloudflare
-- URL Scanner has answered about every URL in it.
--
-- Why the message gets a state instead of the send getting a wait:
--
--   URL Scanner is submit-then-poll. Its result endpoint answers 404 while a
--   scan runs and Cloudflare recommends polling every 10-30 seconds, so the
--   verdict for a URL nobody has scanned before simply cannot be obtained inside
--   an interactive request. The two honest options are to publish first and
--   check later, or to accept the message and withhold it. RF-21 is a blocking
--   control, so it is the second: the row exists, the sender can see their own,
--   and nobody else is shown it until the scan clears.
--
-- Why a new status value and not a boolean column:
--
--   Every read path in this service already filters `status = 'active'` — list,
--   get, search, quote preview, unread counts. A new status is therefore
--   *withholding by construction*: a pending message is invisible to every one
--   of those without a single one of them being edited, and a query added later
--   inherits the same behaviour instead of having to remember a flag. A boolean
--   beside `status = 'active'` would have to be added to each of them by hand,
--   and the one that was forgotten is the one that leaks the message.
--
-- The state machine has exactly two verdict transitions, mirroring RF-22's:
--
--   pending_link_scan --(all urls safe)-----> active
--   pending_link_scan --(any url malicious)-> deleted
--
-- A blocked message becomes `deleted` rather than gaining a third terminal
-- state: it was never published, so to every reader it is a message that does
-- not exist, which is exactly what `deleted` already means to them. The sender
-- is told why by the API, not by a status nobody else may read.

BEGIN;

ALTER TABLE chat.messages
    DROP CONSTRAINT messages_status_check;

ALTER TABLE chat.messages
    ADD CONSTRAINT messages_status_check
    CHECK (status IN ('active', 'deleted', 'pending_link_scan'));

-- ---------------------------------------------------------------------------
-- link_scans: one row per canonical URL, shared by every message that names it
-- ---------------------------------------------------------------------------
--
-- The primary key is the canonical URL itself and not a hash of it, because the
-- worker has to submit the URL to the provider after a restart — a hash would
-- mean storing the URL beside it anyway. Canonicalization (scheme, host, path,
-- query; fragment dropped) happens in libs/go/platform/urlsafety and is the only
-- thing allowed to decide that two spellings are the same resource.
--
-- Keying by URL rather than by message is what deduplicates the scans: ten
-- people pasting the same link produce one submission, and the eleventh gets the
-- verdict from this table without touching Cloudflare at all.
CREATE TABLE chat.link_scans (
    canonical_url    TEXT        PRIMARY KEY,
    -- pending: submitted or awaiting submission; safe/malicious: the provider
    -- answered. There is deliberately no 'failed' state — a failure leaves the
    -- row pending with a later next_attempt_at, because a URL nobody could scan
    -- is a URL that is still not cleared.
    status           TEXT        NOT NULL DEFAULT 'pending',
    -- The provider's scan id, once submitted. NULL means the submission has not
    -- happened or did not succeed, and the next claim will submit.
    scan_uuid        TEXT,
    attempts         SMALLINT    NOT NULL DEFAULT 0,
    -- NULL counts as due, which is what makes a freshly inserted row claimable
    -- by the next worker pass without a second write.
    next_attempt_at  TIMESTAMPTZ,
    -- When the verdict was decided. It is what expires a verdict: a row older
    -- than the shared VerdictTTL is re-scanned rather than trusted, because
    -- reputation changes in both directions.
    decided_at       TIMESTAMPTZ,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT link_scans_status_check CHECK (status IN ('pending', 'safe', 'malicious')),
    CONSTRAINT link_scans_url_length_check CHECK (char_length(canonical_url) <= 2048),
    -- A decided row must say when, and a pending row must not pretend to have.
    CONSTRAINT link_scans_decided_check CHECK (
        (status = 'pending' AND decided_at IS NULL)
        OR (status <> 'pending' AND decided_at IS NOT NULL)
    )
);

-- The worker's claim predicate, and nothing else. Partial so the index is empty
-- whenever there is no backlog, which is what makes idle polling almost free —
-- the same property RF-22's scan queue relies on.
CREATE INDEX idx_link_scans_due
    ON chat.link_scans (next_attempt_at NULLS FIRST, created_at)
    WHERE status = 'pending';

-- Expiry sweeps and verdict reads by freshness.
CREATE INDEX idx_link_scans_decided_at
    ON chat.link_scans (decided_at)
    WHERE status <> 'pending';

-- ---------------------------------------------------------------------------
-- message_link_scans: which URLs a withheld message is waiting on
-- ---------------------------------------------------------------------------
--
-- Without this the worker would have to re-extract URLs from message bodies to
-- find out who was waiting on a verdict, which means re-running the parser over
-- every pending body on every pass and trusting it to produce the same answer it
-- produced at write time. The edge is recorded once, in the same statement that
-- creates the message, so "message created but its links not recorded" is
-- unreachable — the same argument message_attachments makes in 000021.
--
-- ON DELETE CASCADE on the message side only. link_scans rows outlive the
-- messages that caused them, because the verdict is about the URL and the next
-- message naming it should not have to pay for a new scan.
CREATE TABLE chat.message_link_scans (
    message_id    UUID NOT NULL REFERENCES chat.messages (id) ON DELETE CASCADE,
    canonical_url TEXT NOT NULL REFERENCES chat.link_scans (canonical_url) ON DELETE CASCADE,

    PRIMARY KEY (message_id, canonical_url)
);

-- "Which messages are waiting on this URL", the direction the worker reads
-- after a verdict lands. The primary key already serves the other direction.
CREATE INDEX idx_message_link_scans_url
    ON chat.message_link_scans (canonical_url);

-- The pending backlog itself, for the pass that promotes or blocks messages
-- once their URLs are decided. Partial for the same reason as above: it holds
-- nothing at all in the steady state where every message is published.
CREATE INDEX idx_messages_pending_link_scan
    ON chat.messages (created_at)
    WHERE status = 'pending_link_scan';

-- ---------------------------------------------------------------------------
-- link_safety_fingerprint: the promotion is bound to the content it judged
-- ---------------------------------------------------------------------------
--
-- A verdict is about a specific body and a specific set of URLs. Without a
-- binding, a scan started for content A and finishing after the row somehow
-- became content B would publish B on A's clearance — the classic
-- time-of-check/time-of-use gap.
--
-- Editing a withheld message is now refused outright, so this should be
-- unreachable through the API. It exists anyway, as defence in depth: the API
-- rule is one line that a future change could relax by accident, and this is the
-- invariant that would still hold if it did. Fail closed — a mismatch promotes
-- nothing.
--
-- The value is a SHA-256 over a schema tag, the body that was scanned, and the
-- sorted set of canonical URLs; see linkSafetyFingerprint in the service layer.
-- It is a comparison key, not an authentication mechanism.
ALTER TABLE chat.messages
    ADD COLUMN link_safety_fingerprint TEXT;

-- The same value is stamped on each association, so the promotion can require
-- that the URLs it is reading were recorded for the content it is publishing.
ALTER TABLE chat.message_link_scans
    ADD COLUMN fingerprint TEXT NOT NULL DEFAULT '';

-- ---------------------------------------------------------------------------
-- message_publish_outbox: the promotion event, written in the same transaction
-- ---------------------------------------------------------------------------
--
-- Promotion used to be "UPDATE ... COMMIT" followed by a best-effort websocket
-- publish. A crash between the two left a message active in the database that
-- nobody was ever told about: it existed, it was visible on the next read, and
-- no client learned of it until something else caused a refetch.
--
-- The event is therefore written in the same transaction as the state change,
-- and a dispatcher delivers it afterwards. That is the pattern
-- chat.notification_outbox (000006) already established in this schema for
-- mention notifications, and reusing its shape rather than inventing a second
-- one means an operator has one thing to understand.
--
-- Delivery is at-least-once, not exactly-once: the dispatcher can publish and
-- then fail before marking the row delivered, so the same message.created may
-- be broadcast twice. That is safe because the consumer deduplicates by message
-- id — the client upserts, and a message it already holds is replaced, not
-- appended. Claiming exactly-once here would be claiming something the network
-- cannot provide.
CREATE TABLE chat.message_publish_outbox (
    message_id   UUID        PRIMARY KEY REFERENCES chat.messages (id) ON DELETE CASCADE,
    workspace_id UUID        NOT NULL REFERENCES chat.workspaces (id) ON DELETE CASCADE,
    -- Two terminal outcomes need announcing, not one. A promotion goes to the
    -- target; a block goes only to its author, who would otherwise be left
    -- watching a message that never resolves. One table rather than two because
    -- the delivery machinery — claim, retry, retire — is identical, and a second
    -- copy of it would be a second thing to keep correct.
    event_type   TEXT        NOT NULL DEFAULT 'message.created',
    -- Denormalised so the dispatcher can address the broadcast without re-reading
    -- the message and re-deriving which topic it belongs to.
    target_type  TEXT        NOT NULL,
    target_id    UUID        NOT NULL,
    status       TEXT        NOT NULL DEFAULT 'pending',
    attempts     SMALLINT    NOT NULL DEFAULT 0,
    -- NULL counts as due, so a freshly written row is deliverable on the next
    -- pass without a second write.
    next_attempt_at TIMESTAMPTZ,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- `cancelled` is the third outcome the dispatcher needs: a message deleted
    -- between promotion and delivery has nothing left to announce, and without a
    -- terminal state its event stayed pending forever, retried forever, and kept
    -- the backlog gauge climbing over work that could never succeed.
    CONSTRAINT message_publish_outbox_status_check
        CHECK (status IN ('pending', 'delivered', 'cancelled')),
    CONSTRAINT message_publish_outbox_event_check
        CHECK (event_type IN ('message.created', 'message.blocked')),
    -- 'sender' addresses one user rather than a conversation, which is what
    -- keeps a blocked message from being announced to the target it was never
    -- shown in.
    CONSTRAINT message_publish_outbox_target_check
        CHECK (target_type IN ('channel', 'dm', 'sender'))
);

-- The message_id primary key is what makes the promotion idempotent end to end:
-- a message can have exactly one publish event, so even if two replicas somehow
-- both promoted it, only one event exists to deliver.

-- The dispatcher's claim predicate, and nothing else. Partial so the index is
-- empty in the steady state where everything has been delivered.
CREATE INDEX idx_message_publish_outbox_due
    ON chat.message_publish_outbox (next_attempt_at NULLS FIRST, created_at)
    WHERE status = 'pending';

-- ---------------------------------------------------------------------------
-- message_pending_mentions: mentions held back while a message is withheld
-- ---------------------------------------------------------------------------
--
-- Creating a message writes its mentions straight into chat.notification_outbox.
-- For a withheld message that is a side effect aimed at somebody who is not
-- allowed to know the message exists — the rule RF-21 exists to enforce is that
-- a pending message produces nothing for other participants.
--
-- Dropping them instead would be worse: the mention would be lost for good when
-- the scan cleared. So they are parked here, and the promotion moves them into
-- notification_outbox in the same transaction that makes the message
-- publishable. A blocked message simply never reaches that step, and its rows
-- disappear with it.
CREATE TABLE chat.message_pending_mentions (
    message_id UUID NOT NULL REFERENCES chat.messages (id) ON DELETE CASCADE,
    user_id    UUID NOT NULL REFERENCES auth.users (id) ON DELETE CASCADE,

    PRIMARY KEY (message_id, user_id)
);

-- ---------------------------------------------------------------------------
-- create_idempotency_key: retrying a send must not create a second message
-- ---------------------------------------------------------------------------
--
-- Forwarding has been idempotent since 000015. Creating was not, and RF-21 made
-- that gap matter: a message withheld for a link scan returns no visible result
-- to anyone but its author, so a client that retries — a dropped response, a
-- flaky connection, an impatient user — had every reason to send again, and
-- would get a second withheld message and a second scan.
--
-- The key is scoped by workspace, sender and destination, using COALESCE so one
-- index covers both a channel and a DM: a NULL channel_id in a plain composite
-- index would make DM rows all distinct from each other and enforce nothing.
--
-- Scoping by sender is what stops cross-user replay: presenting somebody else's
-- key collides with nothing, so it simply creates the caller's own message.
ALTER TABLE chat.messages
    ADD COLUMN create_idempotency_key VARCHAR(128);

-- The identity the key stands for: a SHA-256 over every field of the create that
-- decides what gets written — body, format, parent, references, attachments and
-- destination.
--
-- Without it a replay could only compare the body, so a key reused with the same
-- text but a different attachment, format or parent returned the original
-- message and the caller believed a new one had been created. See
-- createIdentity in the service layer.
ALTER TABLE chat.messages
    ADD COLUMN create_request_fingerprint TEXT;

CREATE UNIQUE INDEX messages_create_idempotency_unique
    ON chat.messages (
        workspace_id,
        sender_id,
        COALESCE(channel_id, dm_conversation_id),
        create_idempotency_key
    )
    WHERE create_idempotency_key IS NOT NULL;

COMMIT;
