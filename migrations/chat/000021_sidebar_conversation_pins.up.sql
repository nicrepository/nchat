BEGIN;

CREATE TABLE chat.sidebar_conversation_pins (
    user_id UUID NOT NULL REFERENCES auth.users (id) ON DELETE CASCADE,
    workspace_id UUID NOT NULL REFERENCES chat.workspaces (id) ON DELETE CASCADE,
    conversation_type TEXT NOT NULL CHECK (conversation_type IN ('channel', 'dm')),
    conversation_id UUID NOT NULL,
    pinned_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (user_id, workspace_id, conversation_type, conversation_id)
);

CREATE INDEX sidebar_conversation_pins_order_idx
    ON chat.sidebar_conversation_pins (user_id, workspace_id, pinned_at, conversation_id);

COMMIT;
