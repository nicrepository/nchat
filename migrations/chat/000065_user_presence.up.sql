BEGIN;

-- Server-authoritative presence facts per workspace member (issue #798).
--
-- Two things live here and nothing else:
--
--   * the manual state a user chose — available, busy, dnd, brb, away or
--     appear_offline — and when it stops applying. Every manual state expires:
--     a state nobody remembers setting is the failure this column pair exists
--     to prevent, so the state and its expiry are written and cleared together;
--   * last_seen_at, the instant the server last published this user as
--     offline. It is written by the server on that transition and never taken
--     from a client.
--
-- Connectivity is not stored: sessions are ephemeral and live in the realtime
-- layer. The custom status message is not stored either: it belongs to the
-- profile in auth.users, and keeping it apart is the RF-59 separation.
CREATE TABLE chat.user_presence (
    workspace_id      UUID        NOT NULL,
    user_id           UUID        NOT NULL,
    manual_state      TEXT,
    manual_expires_at TIMESTAMPTZ,
    manual_updated_at TIMESTAMPTZ,
    last_seen_at      TIMESTAMPTZ,

    PRIMARY KEY (workspace_id, user_id),
    -- A presence row belongs to a membership, and leaves with its workspace.
    CONSTRAINT user_presence_member_fkey FOREIGN KEY (workspace_id, user_id)
        REFERENCES chat.workspace_members (workspace_id, user_id) ON DELETE CASCADE,
    CONSTRAINT user_presence_manual_state_check CHECK (
        manual_state IS NULL
        OR manual_state IN ('available', 'busy', 'dnd', 'brb', 'away', 'appear_offline')
    ),
    CONSTRAINT user_presence_manual_complete_check CHECK (
        (manual_state IS NULL AND manual_expires_at IS NULL AND manual_updated_at IS NULL)
        OR (manual_state IS NOT NULL AND manual_expires_at IS NOT NULL AND manual_updated_at IS NOT NULL)
    )
);

-- Every read is by (workspace_id, user_id) or (workspace_id, user_id = ANY(...)),
-- which the primary key serves. No further index is needed.

COMMIT;
