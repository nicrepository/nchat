-- 000021_message_attachments.up.sql
-- RF-32: bind an uploaded attachment to the message that carries it.
--
-- Until now an upload was associated with a *destination* (a channel or a DM
-- conversation) and nothing else, so file-service could list "the files of this
-- channel" but no message ever owned one. This table is that missing edge.
--
-- Why an associative table in the chat schema, and not a message_id column on
-- files.attachments:
--
--   - Ownership. chat-service owns chat.messages and every migration under
--     migrations/chat; file-service owns files.attachments and migrations/files.
--     The write that creates the link happens in the same statement that creates
--     the message, which is chat-service's. Putting the column on
--     files.attachments would make chat-service a writer of another service's
--     table, and would put a chat concern in a files migration.
--   - Atomicity. Message and link live in one schema and are written by one
--     statement (see PGXMessageStore.CreateMessage), so "message created but
--     attachment not linked" and "attachment linked to a message that does not
--     exist" are both unreachable, including on rollback. A column on the other
--     table would need a second write to a table the same statement does not own.
--   - Migration order. No cross-schema foreign key is used anywhere in this
--     repository — files/000001 states that convention explicitly and references
--     chat.* by convention only, exactly as chat/000001 references auth.users.id.
--     Keeping to it means migrations/chat and migrations/files stay independent
--     of each other's ordering, and this table's only real FK points at
--     chat.messages, in its own schema.
--
-- The destination-scoped listing file-service already serves is untouched: this
-- edge is additional information, not a replacement for it.
--
-- Design decisions:
--   - message_id has a real FK with ON DELETE CASCADE. A message row is never
--     hard-deleted by the service (deletion is a soft status change), so the
--     cascade only fires when the workspace, channel or conversation above it is
--     removed — the same fate chat.messages itself already has.
--   - attachment_id is a plain UUID for the reason above. Referential integrity
--     is enforced at write time instead: a row is only ever inserted by a
--     statement that has just re-read files.attachments and confirmed the
--     attachment exists, belongs to the same workspace, belongs to exactly this
--     message's destination, was uploaded by this sender, is not failed, not
--     deleted, and is not already linked.
--   - UNIQUE (attachment_id) is what makes "linked twice" impossible — to the
--     same message (also covered by the primary key) and, more importantly, to
--     two different messages. An upload belongs to at most one message, so an
--     attachment cannot be re-posted into another channel by replaying its id.
--   - The primary key leads with message_id, so reading "the attachments of
--     these messages" is an index scan over that column and no separate index is
--     needed. The batch read (chat.message_attachments ANY($1::uuid[])) is the
--     only query shape here, and it is what keeps a page of messages one extra
--     query rather than one per message.
--   - position orders several attachments under one message deterministically.
--     The current product rule is one attachment per message and the API accepts
--     at most one, so this column is bounded rather than open-ended: the CHECK
--     is the schema's statement that this is a small list, not a bucket.
--   - No scan state is copied here. status and preview_status live on
--     files.attachments, are written by file-service alone, and are read through
--     the join — so a verdict never has to be propagated into a second table and
--     the two can never disagree.
--
-- No existing row is read, rewritten or deleted, and no attachment, storage key
-- or key material is touched. Every message that exists keeps behaving exactly
-- as it did: no attachments means no rows here.

BEGIN;

CREATE TABLE chat.message_attachments (
    message_id    UUID        NOT NULL REFERENCES chat.messages (id) ON DELETE CASCADE,
    -- files.attachments.id, by convention. See the header: cross-schema foreign
    -- keys are not used in this repository.
    attachment_id UUID        NOT NULL,
    position      SMALLINT    NOT NULL DEFAULT 0,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT message_attachments_pkey PRIMARY KEY (message_id, attachment_id),
    CONSTRAINT message_attachments_attachment_id_unique UNIQUE (attachment_id),
    CONSTRAINT message_attachments_position_check CHECK (position BETWEEN 0 AND 9)
);

COMMIT;
