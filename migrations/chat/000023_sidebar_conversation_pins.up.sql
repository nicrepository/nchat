BEGIN;

CREATE TABLE chat.sidebar_conversation_pins (
    user_id            UUID NOT NULL,
    workspace_id       UUID NOT NULL REFERENCES chat.workspaces (id) ON DELETE CASCADE,
    channel_id         UUID REFERENCES chat.channels (id) ON DELETE CASCADE,
    dm_conversation_id UUID REFERENCES chat.dm_conversations (id) ON DELETE CASCADE,
    pinned_at          TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT sidebar_conversation_pins_one_target_check CHECK (
        (channel_id IS NOT NULL AND dm_conversation_id IS NULL)
        OR (channel_id IS NULL AND dm_conversation_id IS NOT NULL)
    )
);

CREATE UNIQUE INDEX sidebar_conversation_pins_user_channel_unique
    ON chat.sidebar_conversation_pins (user_id, channel_id)
    WHERE channel_id IS NOT NULL;

CREATE UNIQUE INDEX sidebar_conversation_pins_user_dm_unique
    ON chat.sidebar_conversation_pins (user_id, dm_conversation_id)
    WHERE dm_conversation_id IS NOT NULL;

CREATE INDEX sidebar_conversation_pins_workspace_user_pinned_at
    ON chat.sidebar_conversation_pins (workspace_id, user_id, pinned_at ASC);

COMMIT;
