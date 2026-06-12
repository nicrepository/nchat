-- 000004_chat_messages.up.sql
-- Chat message model foundation.
--
-- Design decisions:
--   - Single unified chat.messages table for both channel and DM messages.
--   - Exactly-one-target CHECK: channel_id XOR dm_conversation_id.
--   - Composite FKs enforce workspace-channel and workspace-DM consistency at
--     the database level (same pattern as migration 000002 for categories).
--   - Nullable self-referencing FKs for quote-reply, forwarding, and references
--     are placeholders; enforcement of same-target rules is done in the service.
--   - Soft delete: status='deleted' + deleted_at; no hard DELETE.
--   - body_text stores plain text now; JSONB rich text is future scope (RF-11).
--   - No reactions, mentions, pins, favorites, attachments, or E2E in this migration.
--   - Partial indexes keep NULL-heavy columns efficient.

BEGIN;

-- ---------------------------------------------------------------------------
-- Composite unique indexes required for workspace-scoped message FKs.
-- Follows the same pattern as 000002 did for channel_categories.
-- ---------------------------------------------------------------------------

ALTER TABLE chat.channels
    ADD CONSTRAINT channels_workspace_id_id_unique UNIQUE (workspace_id, id);

ALTER TABLE chat.dm_conversations
    ADD CONSTRAINT dm_conversations_workspace_id_id_unique UNIQUE (workspace_id, id);

-- ---------------------------------------------------------------------------
-- messages
-- ---------------------------------------------------------------------------

CREATE TABLE chat.messages (
    id                        UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id              UUID        NOT NULL REFERENCES chat.workspaces (id) ON DELETE CASCADE,
    channel_id                UUID,
    dm_conversation_id        UUID,
    sender_id                 UUID        NOT NULL,
    kind                      TEXT        NOT NULL DEFAULT 'user',
    body_text                 TEXT        NOT NULL DEFAULT '',
    status                    TEXT        NOT NULL DEFAULT 'active',
    parent_message_id         UUID        REFERENCES chat.messages (id),
    forwarded_from_message_id UUID        REFERENCES chat.messages (id),
    referenced_message_id     UUID        REFERENCES chat.messages (id),
    edited_at                 TIMESTAMPTZ,
    deleted_at                TIMESTAMPTZ,
    created_at                TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at                TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- Exactly one target: channel_id XOR dm_conversation_id must be non-null.
    CONSTRAINT messages_exactly_one_target CHECK (
        (channel_id IS NULL) <> (dm_conversation_id IS NULL)
    ),

    CONSTRAINT messages_kind_check        CHECK (kind   IN ('user', 'system')),
    CONSTRAINT messages_status_check      CHECK (status IN ('active', 'deleted')),
    CONSTRAINT messages_body_length_check CHECK (char_length(body_text) <= 40000),

    -- Workspace-channel consistency: channel must belong to the same workspace.
    CONSTRAINT messages_workspace_channel_fk
        FOREIGN KEY (workspace_id, channel_id)
        REFERENCES chat.channels (workspace_id, id)
        ON DELETE CASCADE,

    -- Workspace-DM consistency: DM conversation must belong to the same workspace.
    CONSTRAINT messages_workspace_dm_fk
        FOREIGN KEY (workspace_id, dm_conversation_id)
        REFERENCES chat.dm_conversations (workspace_id, id)
        ON DELETE CASCADE
);

-- Channel message list: workspace + channel + time ordering.
CREATE INDEX idx_messages_channel
    ON chat.messages (workspace_id, channel_id, created_at, id)
    WHERE channel_id IS NOT NULL;

-- DM message list: workspace + conversation + time ordering.
CREATE INDEX idx_messages_dm
    ON chat.messages (workspace_id, dm_conversation_id, created_at, id)
    WHERE dm_conversation_id IS NOT NULL;

-- Sender lookup.
CREATE INDEX idx_messages_sender
    ON chat.messages (sender_id);

-- Self-referencing message lookups (sparse, most messages have no references).
CREATE INDEX idx_messages_parent
    ON chat.messages (parent_message_id)
    WHERE parent_message_id IS NOT NULL;

CREATE INDEX idx_messages_forwarded
    ON chat.messages (forwarded_from_message_id)
    WHERE forwarded_from_message_id IS NOT NULL;

CREATE INDEX idx_messages_referenced
    ON chat.messages (referenced_message_id)
    WHERE referenced_message_id IS NOT NULL;

COMMIT;
