BEGIN;

-- edited_at already exists since 000004_chat_messages.
ALTER TABLE chat.messages
    ADD COLUMN edit_count INTEGER NOT NULL DEFAULT 0;

ALTER TABLE chat.workspaces
    ADD COLUMN edit_window_seconds INTEGER NULL DEFAULT 900,
    ADD CONSTRAINT workspaces_edit_window_seconds_check
        CHECK (edit_window_seconds IS NULL OR edit_window_seconds BETWEEN 30 AND 86400);

CREATE TABLE chat.message_edit_history (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    message_id UUID NOT NULL,
    body TEXT NOT NULL,
    body_format TEXT NOT NULL,
    editor_user_id UUID NOT NULL,
    versioned_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT message_edit_history_message_fk
        FOREIGN KEY (message_id) REFERENCES chat.messages (id) ON DELETE CASCADE,
    CONSTRAINT message_edit_history_editor_fk
        FOREIGN KEY (editor_user_id) REFERENCES auth.users (id),
    CONSTRAINT message_edit_history_body_length_check CHECK (char_length(body) <= 40000),
    CONSTRAINT message_edit_history_body_format_check CHECK (body_format IN ('v1', 'v2', 'v3'))
);

CREATE INDEX message_edit_history_message_versioned_idx
    ON chat.message_edit_history (message_id, versioned_at DESC);

COMMIT;
