BEGIN;

-- RF-05 + human scope decision 2026-07-08: pinned messages per channel or DM.
-- Unlike favorites (RF-06, private), a pin is visible to everyone who can read
-- the container. Pin/unpin uses the same read-access gate; no moderator role.
CREATE TABLE chat.message_pins (
    target_type TEXT NOT NULL,
    target_id UUID NOT NULL,
    message_id UUID NOT NULL,
    pinned_by_user_id UUID NOT NULL,
    pinned_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT message_pins_message_fk
        FOREIGN KEY (message_id) REFERENCES chat.messages (id) ON DELETE CASCADE,
    CONSTRAINT message_pins_pinned_by_fk
        FOREIGN KEY (pinned_by_user_id) REFERENCES auth.users (id) ON DELETE CASCADE,
    CONSTRAINT message_pins_target_type_check CHECK (target_type IN ('channel', 'dm')),
    -- One pin per (container, message); the PK also serves the target lookup
    -- prefix and the unique (target_type, target_id, message_id) guard.
    PRIMARY KEY (target_type, target_id, message_id)
);

-- Ordered listing of a container's pins, newest pin first.
CREATE INDEX message_pins_target_pinned_at_idx
    ON chat.message_pins (target_type, target_id, pinned_at DESC, message_id DESC);

-- Supports the message FK cascade and per-message lookups.
CREATE INDEX message_pins_message_id_idx
    ON chat.message_pins (message_id);

COMMIT;
