BEGIN;

CREATE TABLE chat.conversation_read_state (
    user_id             UUID NOT NULL,
    workspace_id        UUID NOT NULL REFERENCES chat.workspaces (id) ON DELETE CASCADE,
    channel_id          UUID REFERENCES chat.channels (id) ON DELETE CASCADE,
    dm_conversation_id  UUID REFERENCES chat.dm_conversations (id) ON DELETE CASCADE,
    last_read_message_id UUID,
    last_read_at        TIMESTAMPTZ NOT NULL,
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT conversation_read_state_one_target_check CHECK (
        (channel_id IS NOT NULL AND dm_conversation_id IS NULL)
        OR (channel_id IS NULL AND dm_conversation_id IS NOT NULL)
    )
);

CREATE UNIQUE INDEX conversation_read_state_user_channel_unique
    ON chat.conversation_read_state (user_id, channel_id)
    WHERE channel_id IS NOT NULL;

CREATE UNIQUE INDEX conversation_read_state_user_dm_unique
    ON chat.conversation_read_state (user_id, dm_conversation_id)
    WHERE dm_conversation_id IS NOT NULL;

COMMIT;
