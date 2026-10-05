-- Issue #1025: durable idempotency for channel creation.
--
-- One row per (workspace, actor, Idempotency-Key). The key is claimed in the
-- same transaction that creates the channel, so the primary key — not the slug
-- and not process memory — decides that a retried or concurrent request with
-- the same key yields at most one channel. request_hash is the server's
-- fingerprint of the normalized request: the same key with another payload is a
-- deterministic conflict, never a second channel.
--
-- channel_id is claimed before the channel row exists, hence the deferred FK:
-- it is checked at commit, when both rows are in place or neither is.
--
-- Blue/Green: additive only. A build that predates this table never reads or
-- writes it; this build writes it only when a client sends Idempotency-Key.
BEGIN;

CREATE TABLE chat.channel_creation_requests (
    workspace_id    UUID        NOT NULL,
    actor_user_id   UUID        NOT NULL,
    idempotency_key TEXT        NOT NULL CHECK (length(idempotency_key) BETWEEN 1 AND 128),
    request_hash    TEXT        NOT NULL CHECK (length(request_hash) = 64),
    channel_id      UUID        NOT NULL UNIQUE
        REFERENCES chat.channels(id) ON DELETE CASCADE DEFERRABLE INITIALLY DEFERRED,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (workspace_id, actor_user_id, idempotency_key)
);

COMMIT;
