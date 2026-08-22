-- 000012_admin_config_versioning.up.sql
-- Configuration & Secrets Management (issue #580): optimistic concurrency and
-- an append-only change history for the platform configuration the Admin
-- Console can actually apply.
--
-- Scope, and why it is this small:
--
--   auth.auth_policy_settings is the only platform-wide configuration document
--   in NChat that is stored in the database and read by the enforcing service
--   on the path that enforces it (auth-service reads it per request in
--   login_store, session_store, user_store, invite_store, password_reset_store
--   and device_session_store). Everything else an operator might want to tune
--   is an environment variable read at boot from the Kustomize ConfigMap or a
--   Sealed Secret, so changing it is a rollout performed through Git and not a
--   row this console may write. Those stay read-only in the Admin API, and no
--   table is created here to pretend otherwise.
--
-- Why the revision lives on the settings row and not in a side table:
--   the console's concurrency control is a compare-and-swap
--   (UPDATE ... WHERE id = 1 AND revision = $expected), and a counter kept in a
--   second row could be bumped by a writer that did not change the values, or
--   left behind by one that did. On the row, the two cannot disagree.
--
-- Why the history stores values as JSONB scalars constrained to
-- number/boolean/null:
--   it is the structural guarantee that no secret can ever enter this trail.
--   Every configuration this console can write is numeric or boolean; a string
--   simply cannot be stored, so a future definition that carries a credential
--   cannot be recorded here by accident. Adding a genuine string-valued,
--   non-sensitive setting is a migration, deliberately.

BEGIN;

SET LOCAL search_path = auth, public;

-- ---------------------------------------------------------------------------
-- auth_policy_settings.revision
--
-- Starts at 1 for the seeded row. Every applied change increments it, and the
-- version row created by that change records the revision it produced, so the
-- history and the live row cannot drift.
-- ---------------------------------------------------------------------------

ALTER TABLE auth.auth_policy_settings
    ADD COLUMN revision INT NOT NULL DEFAULT 1;

ALTER TABLE auth.auth_policy_settings
    ADD CONSTRAINT auth_policy_settings_revision_check CHECK (revision > 0);

-- ---------------------------------------------------------------------------
-- admin_config_versions
--
-- One row per applied change. Failed validations and refused changes are not
-- versions: they never reached the settings row, so recording them here would
-- describe a state the platform was never in. They are audit events instead
-- (auth.admin_audit_events), which is where a refusal belongs.
--
-- document_key is constrained by CHECK to the closed list of configuration
-- documents this console can write, for the same reason
-- admin_role_capabilities constrains its capability column: a value outside
-- the list must be impossible to store, not merely unhandled.
-- ---------------------------------------------------------------------------

CREATE TABLE auth.admin_config_versions (
    id             BIGSERIAL   PRIMARY KEY,
    document_key   TEXT        NOT NULL,
    -- The revision this change produced. Revision 1 is the migration-seeded
    -- baseline and has no version row, so every stored version is above it.
    revision       INT         NOT NULL,
    applied_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- Nullable so deleting a person never erases the fact that the change
    -- happened, exactly as admin_audit_events treats its actor.
    actor_user_id  UUID        REFERENCES auth.users (id) ON DELETE SET NULL,
    correlation_id TEXT,
    reason         TEXT        NOT NULL DEFAULT '',
    -- Set when this version restored an earlier one. Rollback is forward-only:
    -- it appends a new version rather than deleting or rewriting history, so a
    -- rollback is as auditable as the change it undoes and an apply/rollback
    -- loop leaves a trail instead of erasing one.
    reverts_revision INT,

    CONSTRAINT admin_config_versions_document_check CHECK (document_key IN ('auth.policy')),
    CONSTRAINT admin_config_versions_revision_check CHECK (revision > 1),
    CONSTRAINT admin_config_versions_unique UNIQUE (document_key, revision),
    CONSTRAINT admin_config_versions_reverts_check CHECK (
        reverts_revision IS NULL OR (reverts_revision > 0 AND reverts_revision < revision)
    ),
    CONSTRAINT admin_config_versions_reason_length_check CHECK (char_length(reason) <= 500)
);

CREATE INDEX idx_admin_config_versions_recent
    ON auth.admin_config_versions (document_key, applied_at DESC, id DESC);

-- ---------------------------------------------------------------------------
-- admin_config_version_changes
--
-- The fields one version changed, one row each, rather than a JSON blob on the
-- version: the change set is structural data the console reads back to render a
-- diff and to compute a rollback, and it is what the scalar CHECK below is
-- attached to.
-- ---------------------------------------------------------------------------

CREATE TABLE auth.admin_config_version_changes (
    version_id BIGINT NOT NULL REFERENCES auth.admin_config_versions (id) ON DELETE CASCADE,
    config_key TEXT   NOT NULL,
    value_from JSONB  NOT NULL,
    value_to   JSONB  NOT NULL,

    CONSTRAINT admin_config_version_changes_pkey PRIMARY KEY (version_id, config_key),
    CONSTRAINT admin_config_version_changes_key_check CHECK (config_key ~ '^[a-z][a-z0-9_.]{2,79}$'),
    -- The invariant that keeps credentials out of the trail by construction.
    CONSTRAINT admin_config_version_changes_scalar_check CHECK (
        jsonb_typeof(value_from) IN ('number', 'boolean', 'null')
        AND jsonb_typeof(value_to) IN ('number', 'boolean', 'null')
    ),
    -- A recorded change that changed nothing is not a change.
    CONSTRAINT admin_config_version_changes_distinct_check CHECK (value_from IS DISTINCT FROM value_to)
);

COMMIT;
