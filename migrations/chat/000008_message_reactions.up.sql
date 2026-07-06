BEGIN;

CREATE TABLE chat.message_reactions (
    message_id UUID NOT NULL,
    user_id UUID NOT NULL,
    emoji TEXT NOT NULL CHECK (char_length(emoji) BETWEEN 1 AND 8),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT message_reactions_message_fk
        FOREIGN KEY (message_id) REFERENCES chat.messages (id) ON DELETE CASCADE,
    CONSTRAINT message_reactions_user_fk
        FOREIGN KEY (user_id) REFERENCES auth.users (id) ON DELETE CASCADE,
    PRIMARY KEY (message_id, user_id, emoji)
);

COMMIT;
