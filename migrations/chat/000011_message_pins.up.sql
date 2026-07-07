BEGIN;

-- RF-05: pinned messages per channel. Unlike favorites (RF-06, private), a pin
-- is visible to everyone who can read the channel and requires an elevated role
-- to create/remove (enforced in the service, not here).
CREATE TABLE chat.message_pins (
    channel_id UUID NOT NULL,
    message_id UUID NOT NULL,
    pinned_by_user_id UUID NOT NULL,
    pinned_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT message_pins_channel_fk
        FOREIGN KEY (channel_id) REFERENCES chat.channels (id) ON DELETE CASCADE,
    CONSTRAINT message_pins_message_fk
        FOREIGN KEY (message_id) REFERENCES chat.messages (id) ON DELETE CASCADE,
    CONSTRAINT message_pins_pinned_by_fk
        FOREIGN KEY (pinned_by_user_id) REFERENCES auth.users (id) ON DELETE CASCADE,
    -- One pin per (channel, message); the PK also serves the channel_id lookup
    -- prefix and the unique (channel_id, message_id) guard.
    PRIMARY KEY (channel_id, message_id)
);

-- Ordered listing of a channel's pins, newest pin first.
CREATE INDEX message_pins_channel_pinned_at_idx
    ON chat.message_pins (channel_id, pinned_at DESC, message_id DESC);

-- Supports the message FK cascade and per-message lookups.
CREATE INDEX message_pins_message_id_idx
    ON chat.message_pins (message_id);

COMMIT;
