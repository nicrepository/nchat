BEGIN;

ALTER TABLE chat.messages
    DROP CONSTRAINT messages_body_format_check,
    ADD CONSTRAINT messages_body_format_check CHECK (body_format IN ('v1', 'v2', 'v3'));

CREATE TABLE chat.notification_outbox (
    id                UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id      UUID        NOT NULL REFERENCES chat.workspaces (id) ON DELETE CASCADE,
    message_id        UUID        NOT NULL REFERENCES chat.messages (id) ON DELETE CASCADE,
    recipient_user_id UUID        NOT NULL REFERENCES auth.users (id) ON DELETE CASCADE,
    kind              TEXT        NOT NULL,
    status            TEXT        NOT NULL DEFAULT 'pending',
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT notification_outbox_kind_check CHECK (kind IN ('mention')),
    CONSTRAINT notification_outbox_status_check
        CHECK (status IN ('pending', 'processing', 'sent', 'failed')),
    CONSTRAINT notification_outbox_message_recipient_unique
        UNIQUE (message_id, recipient_user_id, kind)
);

CREATE INDEX idx_notification_outbox_pending
    ON chat.notification_outbox (created_at, id)
    WHERE status = 'pending';

COMMIT;
