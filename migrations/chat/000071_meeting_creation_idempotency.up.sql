-- Issue #1053: durable create claims, committed with the meeting and roster.
-- Blue/Green: additive; older releases never access this table.
BEGIN;

CREATE TABLE chat.meeting_creation_requests (
    workspace_id    UUID        NOT NULL,
    actor_user_id   UUID        NOT NULL,
    idempotency_key TEXT        NOT NULL CHECK (idempotency_key ~ '^[A-Za-z0-9._:-]{1,128}$'),
    request_hash    TEXT        NOT NULL CHECK (request_hash ~ '^[0-9a-f]{64}$'),
    meeting_id      UUID        NOT NULL UNIQUE
        REFERENCES chat.meetings(id) ON DELETE CASCADE DEFERRABLE INITIALLY DEFERRED,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (workspace_id, actor_user_id, idempotency_key)
);

COMMIT;
