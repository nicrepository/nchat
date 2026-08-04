-- 000020_workspace_max_upload_bytes.up.sql
-- RF-32 (issue #458): make the maximum attachment size configurable per
-- workspace by an administrator.
--
-- The limit already existed, but as file-service deployment configuration
-- (FILE_MAX_UPLOAD_BYTES, default 50 MiB), which only an operator with a
-- redeploy could change. The column below moves the product policy to the same
-- place every other administrative chat setting lives, so RF-32's "configurable
-- by the admin" is satisfied by the mechanism the project already has
-- (migration 000018 did exactly this for the RF-19 anti-spam limit) rather than
-- by a second, parallel settings subsystem.
--
-- The value is bytes and the unit is binary: 262144000 = 250 MiB, which is the
-- RF-32 default of "250 MB" read the way every other size in this repository is
-- written (50 << 20, 5 << 20). It is also the more permissive reading, so a
-- file a user calls 250 MB (250,000,000 bytes) fits. Only whole multiples of
-- 1 MiB are accepted — see the CHECK below.
--
-- The column is added, not altered, so there is no legacy value to reconcile
-- against the new CHECK: every existing row takes the default, which satisfies
-- it (262144000 = 250 x 1048576). No row is rewritten and no value is rounded.
--
-- BIGINT rather than INTEGER: the ceiling below already sits at 536870912,
-- comfortably inside INTEGER, but a size in bytes has no business being one
-- product decision away from overflowing its column.
--
-- NOT NULL rather than nullable, for the same reason as 000018: "no size limit"
-- is not a state a workspace may be in, so every row carries a real value and
-- the fallback lives in the CHECK rather than in the reader.
--
-- Adding a NOT NULL column with a constant default does not rewrite the table
-- on PostgreSQL 11+; the default is stored in the catalog and materialised
-- lazily. chat.workspaces holds one row per workspace in any case.

BEGIN;

ALTER TABLE chat.workspaces
    ADD COLUMN max_upload_bytes BIGINT NOT NULL DEFAULT 262144000,
    -- Bounds mirror libs/go/platform/uploadpolicy (Min/MaxMaxUploadBytes),
    -- which chat-service validates against and file-service enforces with.
    -- The floor is 1 MiB and not 0: a zero cap would let an administrator
    -- disable attachments entirely through a control whose stated purpose is
    -- sizing them. There is no "unlimited" value either — a request large
    -- enough to tie up a service instance indefinitely has to stay
    -- inexpressible.
    ADD CONSTRAINT workspaces_max_upload_bytes_check
        CHECK (
            max_upload_bytes BETWEEN 1048576 AND 536870912
            -- The policy is a whole number of MiB, not an arbitrary byte count.
            -- The administrative UI edits whole MiB, so a value like 1572864
            -- (1.5 MiB) could not be shown there without being changed, and
            -- rounding an administrator's stored limit into a different one is
            -- worse than refusing the value. uploadpolicy.Valid enforces the
            -- same two halves in Go; this is the backstop.
            AND max_upload_bytes % 1048576 = 0
        );

COMMIT;
