-- 000003_chat_dm_conversations.up.sql
-- Direct and ad-hoc group DM foundation.
--
-- Design decisions:
--   - DMs use separate tables from channels.
--   - user_id columns reference auth.users.id by convention (no cross-schema FK).
--   - direct_pair_key is an internal deterministic key for 1:1 uniqueness only.
--   - One direct DM is allowed per unordered user pair per workspace, including
--     archived conversations; storage reactivates an archived direct DM instead
--     of creating a duplicate.

BEGIN;

-- ---------------------------------------------------------------------------
-- dm_conversations
-- ---------------------------------------------------------------------------

CREATE TABLE chat.dm_conversations (
    id              UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id    UUID        NOT NULL REFERENCES chat.workspaces (id) ON DELETE CASCADE,
    type            TEXT        NOT NULL,
    title           TEXT,
    status          TEXT        NOT NULL DEFAULT 'active',
    created_by      UUID        NOT NULL,
    direct_pair_key TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT dm_conversations_type_check CHECK (type IN ('direct', 'group')),
    CONSTRAINT dm_conversations_status_check CHECK (status IN ('active', 'archived')),
    CONSTRAINT dm_conversations_title_length_check CHECK (title IS NULL OR char_length(title) <= 120),
    CONSTRAINT dm_conversations_direct_pair_key_check CHECK (
        (type = 'direct' AND direct_pair_key IS NOT NULL)
        OR
        (type = 'group' AND direct_pair_key IS NULL)
    )
);

CREATE INDEX idx_dm_conversations_workspace
    ON chat.dm_conversations (workspace_id, status, updated_at DESC);

CREATE INDEX idx_dm_conversations_active
    ON chat.dm_conversations (workspace_id, updated_at DESC)
    WHERE status = 'active';

CREATE UNIQUE INDEX idx_dm_conversations_direct_pair_unique
    ON chat.dm_conversations (workspace_id, direct_pair_key)
    WHERE type = 'direct';

-- ---------------------------------------------------------------------------
-- dm_members
-- ---------------------------------------------------------------------------

CREATE TABLE chat.dm_members (
    conversation_id UUID        NOT NULL REFERENCES chat.dm_conversations (id) ON DELETE CASCADE,
    user_id         UUID        NOT NULL,
    role            TEXT        NOT NULL DEFAULT 'member',
    status          TEXT        NOT NULL DEFAULT 'active',
    joined_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    left_at         TIMESTAMPTZ,

    PRIMARY KEY (conversation_id, user_id),
    CONSTRAINT dm_members_role_check CHECK (role IN ('member')),
    CONSTRAINT dm_members_status_check CHECK (status IN ('active', 'left')),
    CONSTRAINT dm_members_left_at_check CHECK (
        (status = 'active' AND left_at IS NULL)
        OR
        (status = 'left' AND left_at IS NOT NULL)
    )
);

CREATE INDEX idx_dm_members_user
    ON chat.dm_members (user_id, status);

CREATE INDEX idx_dm_members_conversation_active
    ON chat.dm_members (conversation_id, user_id)
    WHERE status = 'active';

COMMIT;
