-- 000020_workspace_max_upload_bytes.down.sql
-- Rolling back drops the per-workspace policy. Enforcement then falls back to
-- uploadpolicy.DefaultMaxUploadBytes (250 MiB) — the same value the column was
-- seeded with — because file-service reads a missing or out-of-range policy as
-- the default, never as "no limit". The CHECK constraint is dropped with the
-- column it constrains.

BEGIN;

ALTER TABLE chat.workspaces
    DROP COLUMN IF EXISTS max_upload_bytes;

COMMIT;
