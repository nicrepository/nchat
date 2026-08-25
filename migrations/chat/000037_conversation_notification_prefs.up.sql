BEGIN;

-- Per-user notification preference for one conversation (issue #527).
--
-- Muting is an individual preference and never conversation state: the row is
-- keyed by user_id, so one member silencing a channel changes nothing for
-- anyone else. Same shape and same reasoning as
-- chat.sidebar_conversation_pins — one target per row, channel XOR DM, and a
-- partial unique index per target kind.
--
-- The absence of a row means "not muted". Unmuting deletes rather than writing
-- false, so the table only ever holds the preferences someone actually
-- expressed, and a conversation the user never touched costs nothing.
CREATE TABLE chat.conversation_notification_prefs (
    user_id            UUID NOT NULL,
    workspace_id       UUID NOT NULL REFERENCES chat.workspaces (id) ON DELETE CASCADE,
    channel_id         UUID REFERENCES chat.channels (id) ON DELETE CASCADE,
    dm_conversation_id UUID REFERENCES chat.dm_conversations (id) ON DELETE CASCADE,
    muted_at           TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT conversation_notification_prefs_one_target_check CHECK (
        (channel_id IS NOT NULL AND dm_conversation_id IS NULL)
        OR (channel_id IS NULL AND dm_conversation_id IS NOT NULL)
    )
);

CREATE UNIQUE INDEX conversation_notification_prefs_user_channel_unique
    ON chat.conversation_notification_prefs (user_id, channel_id)
    WHERE channel_id IS NOT NULL;

CREATE UNIQUE INDEX conversation_notification_prefs_user_dm_unique
    ON chat.conversation_notification_prefs (user_id, dm_conversation_id)
    WHERE dm_conversation_id IS NOT NULL;

-- The sidebar reads every preference of one (workspace, user) in a single pass,
-- exactly like the pin listing does.
CREATE INDEX conversation_notification_prefs_workspace_user
    ON chat.conversation_notification_prefs (workspace_id, user_id);

COMMIT;
