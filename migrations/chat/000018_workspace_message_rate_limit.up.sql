-- 000018_workspace_message_rate_limit.up.sql
-- RF-19 (issue #419): make the per-user message rate limit configurable per
-- workspace by an administrator.
--
-- The limit already existed, but as a Go constant (msgPostRateLimit = 60 in
-- services/chat-service/internal/http/router.go). The default below is that
-- same 60 so an existing deployment keeps the exact behaviour it has today
-- until an admin changes it.
--
-- NOT NULL rather than nullable: edit_window_seconds (000013) is nullable
-- because NULL there means "no edit window configured". There is no equivalent
-- meaning here — "no anti-spam limit" is not a state a workspace may be in, so
-- the column carries a real value on every row and the fallback lives in the
-- CHECK below rather than in the reader.
--
-- Adding a NOT NULL column with a constant default does not rewrite the table
-- on PostgreSQL 11+; the default is stored in the catalog and materialised
-- lazily. chat.workspaces holds one row per workspace in any case.

BEGIN;

ALTER TABLE chat.workspaces
    ADD COLUMN message_rate_limit_per_minute INTEGER NOT NULL DEFAULT 60,
    -- Bounds mirror domain.MinMessageRateLimitPerMinute /
    -- MaxMessageRateLimitPerMinute. The lower bound is 1, never 0: zero would
    -- let an admin mute an entire workspace through a control whose stated
    -- purpose is anti-spam, and there is no upper "unlimited" value either —
    -- removing an availability control has to be a deliberate product
    -- decision, not a number typed into this field.
    ADD CONSTRAINT workspaces_message_rate_limit_per_minute_check
        CHECK (message_rate_limit_per_minute BETWEEN 1 AND 600);

COMMIT;
