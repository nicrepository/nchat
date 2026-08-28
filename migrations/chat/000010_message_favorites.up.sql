BEGIN;

-- RF-06: per-user private message favorites. Never visible to other users;
-- exposure is limited to the owner via GET /api/chat/favorites.
CREATE TABLE chat.message_favorites (
    user_id UUID NOT NULL,
    message_id UUID NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT message_favorites_user_fk
        FOREIGN KEY (user_id) REFERENCES auth.users (id) ON DELETE CASCADE,
    CONSTRAINT message_favorites_message_fk
        FOREIGN KEY (message_id) REFERENCES chat.messages (id) ON DELETE CASCADE,
    PRIMARY KEY (user_id, message_id)
);

-- Keyset pagination for the owner's favorites list (newest favorite first).
-- The primary key already provides the unique (user_id, message_id) guard and
-- the plain user_id lookup prefix.
CREATE INDEX message_favorites_user_created_idx
    ON chat.message_favorites (user_id, created_at DESC, message_id DESC);

-- Supports FK cascade and per-message lookups (EXISTS batch in list queries).
CREATE INDEX message_favorites_message_id_idx
    ON chat.message_favorites (message_id);

COMMIT;
