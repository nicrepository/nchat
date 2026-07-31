-- RF-23: authoritative one-to-one call signaling lifecycle.

BEGIN;

CREATE TABLE chat.calls (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL REFERENCES chat.workspaces (id),
    request_id UUID NOT NULL,
    caller_id UUID NOT NULL,
    callee_id UUID NOT NULL,
    call_type TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'ringing',
    version BIGINT NOT NULL DEFAULT 1,
    created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    expires_at TIMESTAMPTZ NOT NULL,
    accepted_at TIMESTAMPTZ,
    ended_at TIMESTAMPTZ,
    CHECK (caller_id <> callee_id),
    CHECK (call_type IN ('audio', 'video')),
    CHECK (status IN ('ringing', 'active', 'declined', 'cancelled', 'timed_out', 'ended')),
    CHECK (version > 0),
    UNIQUE (workspace_id, caller_id, request_id)
);

CREATE INDEX calls_due_ringing_idx
    ON chat.calls (expires_at, id)
    WHERE status = 'ringing';

CREATE INDEX calls_workspace_caller_status_idx
    ON chat.calls (workspace_id, caller_id, status);

CREATE INDEX calls_workspace_callee_status_idx
    ON chat.calls (workspace_id, callee_id, status);

COMMIT;
