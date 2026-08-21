-- 000008_admin_console_rbac.up.sql
-- Admin Console foundation (issue #578): platform administrators, granular
-- administrative capabilities, administrative sessions and the audit trail
-- the Admin API writes.
--
-- Why the auth schema and not a new one:
--   - every row here points at auth.users; a separate schema would either
--     duplicate identity or hold a cross-schema foreign key for no gain;
--   - the repository's migration runner orders files by path, so a new domain
--     directory would run before migrations/auth and the references would not
--     resolve.
--
-- Security decisions:
--   - capabilities are constrained by CHECK to a closed list. A capability the
--     platform does not define cannot be granted, so an unknown string in the
--     database is impossible rather than merely unhandled;
--   - admin_principals is a separate row from auth.users: being a user grants
--     no administrative authority, and revoking it is a delete here, not a
--     change to the person's account;
--   - admin_sessions stores only a hash of the session credential, and carries
--     both an idle and an absolute expiry so the administrative session policy
--     is enforced by the same row the request is authorized against;
--   - every admin session references the auth session that established it, so
--     revoking the underlying login revokes the administrative session with it.

BEGIN;

SET LOCAL search_path = auth, public;

-- ---------------------------------------------------------------------------
-- admin_roles
-- ---------------------------------------------------------------------------

CREATE TABLE auth.admin_roles (
    slug        TEXT        PRIMARY KEY,
    description TEXT        NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT admin_roles_slug_check CHECK (slug ~ '^[a-z][a-z0-9-]{1,63}$')
);

-- ---------------------------------------------------------------------------
-- admin_role_capabilities
--
-- The CHECK is the fail-closed boundary of the whole authorization model: the
-- effective capabilities of a principal are the union of the rows reachable
-- from this table, so a value outside the list cannot enter the system at all.
-- Adding a capability is a migration, deliberately.
-- ---------------------------------------------------------------------------

CREATE TABLE auth.admin_role_capabilities (
    role_slug  TEXT NOT NULL REFERENCES auth.admin_roles (slug) ON DELETE CASCADE,
    capability TEXT NOT NULL,

    CONSTRAINT admin_role_capabilities_pkey PRIMARY KEY (role_slug, capability),
    CONSTRAINT admin_role_capabilities_known_check CHECK (
        capability IN (
            'admin.superuser',
            'admin.users.read',
            'admin.users.manage',
            'admin.channels.read',
            'admin.channels.manage',
            'admin.security.read',
            'admin.security.manage',
            'admin.integrations.read',
            'admin.integrations.manage',
            'admin.infrastructure.read',
            'admin.infrastructure.manage',
            'admin.audit.read',
            'admin.config.read',
            'admin.config.manage'
        )
    )
);

-- ---------------------------------------------------------------------------
-- admin_principals
-- ---------------------------------------------------------------------------

CREATE TABLE auth.admin_principals (
    user_id    UUID        PRIMARY KEY REFERENCES auth.users (id) ON DELETE CASCADE,
    status     TEXT        NOT NULL DEFAULT 'active',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT admin_principals_status_check CHECK (status IN ('active', 'suspended'))
);

-- ---------------------------------------------------------------------------
-- admin_principal_roles
-- ---------------------------------------------------------------------------

CREATE TABLE auth.admin_principal_roles (
    user_id    UUID        NOT NULL REFERENCES auth.admin_principals (user_id) ON DELETE CASCADE,
    role_slug  TEXT        NOT NULL REFERENCES auth.admin_roles (slug) ON DELETE RESTRICT,
    granted_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    granted_by UUID        REFERENCES auth.users (id) ON DELETE SET NULL,

    CONSTRAINT admin_principal_roles_pkey PRIMARY KEY (user_id, role_slug)
);

-- ---------------------------------------------------------------------------
-- admin_sessions
--
-- The credential itself never lands here: session_hash is an HMAC of the
-- opaque value handed to the browser in an HttpOnly cookie.
--
-- Declared after admin_principals because it references it.
-- ---------------------------------------------------------------------------

CREATE TABLE auth.admin_sessions (
    id                  UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    -- References the principal, not the user. An administrative session of
    -- someone who is not an administrator is not a thing this system has, and
    -- the reference is what makes that structural rather than merely enforced.
    --
    -- It closes a real gap: the handshake reads the principal and then writes
    -- the session in two statements, so a principal deleted in between would
    -- otherwise leave an orphan row behind. Here the INSERT simply fails.
    -- Deleting a principal now also removes their sessions instead of leaving
    -- rows that every request would refuse anyway.
    --
    -- Deleting the user still cascades through: auth.users -> admin_principals
    -- -> admin_sessions.
    user_id             UUID        NOT NULL REFERENCES auth.admin_principals (user_id) ON DELETE CASCADE,
    auth_session_id     UUID        NOT NULL REFERENCES auth.user_sessions (id) ON DELETE CASCADE,
    session_hash        TEXT        NOT NULL,
    ip_address          INET,
    user_agent          TEXT,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    idle_expires_at     TIMESTAMPTZ NOT NULL,
    absolute_expires_at TIMESTAMPTZ NOT NULL,
    revoked_at          TIMESTAMPTZ,
    revoked_reason      TEXT,

    CONSTRAINT admin_sessions_hash_unique UNIQUE (session_hash),
    CONSTRAINT admin_sessions_lifetime_check CHECK (absolute_expires_at > created_at)
);

-- Lookup by credential is the only hot path; the unique constraint above
-- already indexes it. These two support revocation cascades.
CREATE INDEX idx_admin_sessions_user_revoked ON auth.admin_sessions (user_id, revoked_at);
CREATE INDEX idx_admin_sessions_auth_session ON auth.admin_sessions (auth_session_id);

-- ---------------------------------------------------------------------------
-- admin_audit_events
--
-- Deliberately narrow. metadata is a JSONB object written only from
-- server-derived values; no request header, token or chat content is ever
-- recorded (see SECURITY.md).
-- ---------------------------------------------------------------------------

CREATE TABLE auth.admin_audit_events (
    id             BIGSERIAL   PRIMARY KEY,
    occurred_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    actor_user_id  UUID        REFERENCES auth.users (id) ON DELETE SET NULL,
    action         TEXT        NOT NULL,
    resource       TEXT,
    result         TEXT        NOT NULL,
    correlation_id TEXT,
    metadata       JSONB       NOT NULL DEFAULT '{}'::jsonb,

    CONSTRAINT admin_audit_events_result_check CHECK (result IN ('success', 'denied', 'error')),
    CONSTRAINT admin_audit_events_action_check CHECK (action <> ''),
    CONSTRAINT admin_audit_events_metadata_object_check CHECK (jsonb_typeof(metadata) = 'object')
);

CREATE INDEX idx_admin_audit_events_occurred ON auth.admin_audit_events (occurred_at DESC, id DESC);
CREATE INDEX idx_admin_audit_events_actor ON auth.admin_audit_events (actor_user_id, occurred_at DESC);

-- ---------------------------------------------------------------------------
-- Seed roles
--
-- Two roles, both of which the platform can explain. No principal is created:
-- the first administrator is granted out of band by an operator (see
-- docs/runbooks/task-admin-console-foundation.md), which is what keeps the
-- bootstrap off the browser and out of any bundle.
-- ---------------------------------------------------------------------------

INSERT INTO auth.admin_roles (slug, description) VALUES
    ('platform-superuser', 'Full platform administration.'),
    ('platform-auditor', 'Read-only platform administration and audit access.');

INSERT INTO auth.admin_role_capabilities (role_slug, capability) VALUES
    ('platform-superuser', 'admin.superuser'),
    ('platform-auditor', 'admin.audit.read'),
    ('platform-auditor', 'admin.users.read'),
    ('platform-auditor', 'admin.channels.read'),
    ('platform-auditor', 'admin.security.read'),
    ('platform-auditor', 'admin.integrations.read'),
    ('platform-auditor', 'admin.infrastructure.read'),
    ('platform-auditor', 'admin.config.read');

COMMIT;
