-- 000001_chat_domain_schema.up.sql
-- Chat domain: workspaces, channel categories, channels, workspace members,
-- channel members, and seed data for MVP default workspace + #geral channel.
--
-- Design decisions:
--   - chat schema keeps tables separate from auth and public namespaces.
--   - user_id columns reference auth.users.id by convention (no cross-schema FK).
--   - Stable UUIDs for seed rows; ON CONFLICT (id) DO NOTHING makes inserts idempotent.
--   - Partial UNIQUE index enforces exactly one is_general channel per workspace.

BEGIN;

CREATE SCHEMA IF NOT EXISTS chat;
SET LOCAL search_path = chat, public;

-- ---------------------------------------------------------------------------
-- workspaces
-- ---------------------------------------------------------------------------

CREATE TABLE chat.workspaces (
    id          UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    slug        TEXT        NOT NULL,
    name        TEXT        NOT NULL,
    status      TEXT        NOT NULL DEFAULT 'active',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT workspaces_slug_unique  UNIQUE (slug),
    CONSTRAINT workspaces_status_check CHECK (status IN ('active', 'disabled'))
);

CREATE INDEX idx_workspaces_status ON chat.workspaces (status);

-- ---------------------------------------------------------------------------
-- channel_categories
-- ---------------------------------------------------------------------------

CREATE TABLE chat.channel_categories (
    id           UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID        NOT NULL REFERENCES chat.workspaces (id) ON DELETE CASCADE,
    name         TEXT        NOT NULL,
    position     INT         NOT NULL DEFAULT 0,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_channel_categories_workspace ON chat.channel_categories (workspace_id);

-- ---------------------------------------------------------------------------
-- channels
-- ---------------------------------------------------------------------------

CREATE TABLE chat.channels (
    id           UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID        NOT NULL REFERENCES chat.workspaces (id) ON DELETE CASCADE,
    category_id  UUID        REFERENCES chat.channel_categories (id) ON DELETE SET NULL,
    slug         TEXT        NOT NULL,
    display_name TEXT        NOT NULL,
    type         TEXT        NOT NULL,
    status       TEXT        NOT NULL DEFAULT 'active',
    is_general   BOOLEAN     NOT NULL DEFAULT false,
    position     INT         NOT NULL DEFAULT 0,
    created_by   UUID,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT channels_workspace_slug_unique UNIQUE (workspace_id, slug),
    CONSTRAINT channels_type_check   CHECK (type   IN ('public', 'private')),
    CONSTRAINT channels_status_check CHECK (status IN ('active', 'archived'))
);

-- At most one #geral channel per workspace.
CREATE UNIQUE INDEX idx_channels_one_general_per_workspace
    ON chat.channels (workspace_id)
    WHERE is_general = true;

CREATE INDEX idx_channels_workspace_status ON chat.channels (workspace_id, status);

-- ---------------------------------------------------------------------------
-- workspace_members
-- ---------------------------------------------------------------------------

CREATE TABLE chat.workspace_members (
    workspace_id UUID        NOT NULL REFERENCES chat.workspaces (id) ON DELETE CASCADE,
    user_id      UUID        NOT NULL,
    role         TEXT        NOT NULL DEFAULT 'member',
    status       TEXT        NOT NULL DEFAULT 'active',
    joined_at    TIMESTAMPTZ NOT NULL DEFAULT now(),

    PRIMARY KEY (workspace_id, user_id),
    CONSTRAINT workspace_members_role_check   CHECK (role   IN ('owner', 'admin', 'member', 'guest')),
    CONSTRAINT workspace_members_status_check CHECK (status IN ('active', 'suspended', 'left'))
);

CREATE INDEX idx_workspace_members_user ON chat.workspace_members (user_id);

-- ---------------------------------------------------------------------------
-- channel_members
-- ---------------------------------------------------------------------------

CREATE TABLE chat.channel_members (
    channel_id UUID        NOT NULL REFERENCES chat.channels (id) ON DELETE CASCADE,
    user_id    UUID        NOT NULL,
    role       TEXT        NOT NULL DEFAULT 'member',
    joined_at  TIMESTAMPTZ NOT NULL DEFAULT now(),

    PRIMARY KEY (channel_id, user_id),
    CONSTRAINT channel_members_role_check CHECK (role IN ('member', 'moderator'))
);

CREATE INDEX idx_channel_members_user ON chat.channel_members (user_id);

-- ---------------------------------------------------------------------------
-- Seed: default workspace and #geral channel
-- Stable UUIDs + ON CONFLICT (id) DO NOTHING make this idempotent.
-- ---------------------------------------------------------------------------

INSERT INTO chat.workspaces (id, slug, name, status)
VALUES ('00000000-0000-0000-0000-000000000001', 'default', 'NChat', 'active')
ON CONFLICT (id) DO NOTHING;

INSERT INTO chat.channels (id, workspace_id, slug, display_name, type, is_general)
VALUES (
    '00000000-0000-0000-0000-000000000002',
    '00000000-0000-0000-0000-000000000001',
    'geral',
    'Geral',
    'public',
    true
)
ON CONFLICT (id) DO NOTHING;

COMMIT;
