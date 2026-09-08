BEGIN;

-- Web Push subscriptions, one row per browser/device instance (issue #745).
--
-- # Why this table lives in the chat schema
--
-- notification-service already owns chat.notification_outbox (000006/000042/
-- 000044) and reads chat.conversation_notification_prefs (000037): the schema
-- boundary in this repository is the *database contract*, not the service that
-- happens to serve an HTTP route. The empty migrations/notifications directory
-- is not that contract yet — scripts/db/grant-runtime.sql grants nchat_app on
-- auth, chat and files only, and the production bootstrap reconciles ownership
-- for those same three schemas. A table created in a fourth schema would be
-- unreadable by every service until those two files and both overlays changed,
-- which is a platform change and not this issue's.
--
-- # Identity
--
-- (workspace_id, user_id, device_id) is the logical identity: one subscription
-- per browser/device instance, per user, per workspace. It is deliberately not
-- "one subscription per user" — a person with a laptop and a phone, or two
-- browsers on one machine, holds several at once and every one of them has to
-- keep working.
--
-- endpoint is unique on its own, and that is the ownership guard rather than a
-- second identity. A Web Push endpoint is a capability URL: whoever can POST to
-- it can push to that browser. If a registration arrives carrying an endpoint
-- another row already holds, the insert violates this index and the request
-- fails — it never rewrites user_id or workspace_id, which is exactly the
-- "endpoint stolen by UPSERT" the design refuses to allow.
--
-- # Generation
--
-- A subscription has a stable id for its whole life, but the endpoint and keys
-- behind it are replaced whenever the browser re-subscribes. generation names
-- *that* — one endpoint/key lifetime — and it is the compare-and-set token a
-- delivery attempt carries, exactly as chat.link_scans.submit_generation is for
-- a submission (000025).
--
-- It exists because a delivery attempt and its answer are not simultaneous. An
-- attempt starts against the endpoint the row held at the time; while it is in
-- flight the browser can re-register and replace that endpoint; the answer then
-- arrives describing an endpoint the row no longer has. Without this column a
-- late 410 would retire a subscription that is working, and a late success or
-- failure would write history belonging to an endpoint that is gone.
--
-- It starts at 1 and only ever increases, so "which generation is this" is
-- answerable without consulting a clock. A timestamp cannot serve here: two
-- writes inside one now() are indistinguishable, and updated_at moves for
-- bookkeeping that changes no endpoint at all.
--
-- # Lifecycle
--
-- active    deliverable.
-- invalid   the provider said the endpoint is gone (404/410). Terminal for
--           delivery; the row is kept for diagnosis and reconcile.
-- disabled  the owner turned it off.
--
-- 429, 5xx and timeouts never reach either terminal state: they raise
-- failure_count and leave the row active, because a subscription discarded on a
-- provider hiccup silently stops notifying a real person.
--
-- The lifecycle CHECK ties the three columns together, so "not active" can only
-- ever be recorded together with when and why. Deliberately no ordering CHECK
-- between the timestamps: every one of them is written as now() by the server
-- and none is client-supplied, so such a constraint would guard nothing and
-- would turn a backwards clock step into a write failure.
CREATE TABLE chat.push_subscriptions (
    id                  UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id        UUID        NOT NULL REFERENCES chat.workspaces (id) ON DELETE CASCADE,
    user_id             UUID        NOT NULL REFERENCES auth.users (id) ON DELETE CASCADE,
    device_id           TEXT        NOT NULL,
    endpoint            TEXT        NOT NULL,
    p256dh              TEXT        NOT NULL,
    auth                TEXT        NOT NULL,
    status              TEXT        NOT NULL DEFAULT 'active',
    generation          BIGINT      NOT NULL DEFAULT 1,
    failure_count       INTEGER     NOT NULL DEFAULT 0,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_success_at     TIMESTAMPTZ,
    invalidated_at      TIMESTAMPTZ,
    invalidation_reason TEXT,

    CONSTRAINT push_subscriptions_status_check
        CHECK (status IN ('active', 'invalid', 'disabled')),
    CONSTRAINT push_subscriptions_failure_count_check
        CHECK (failure_count >= 0),
    -- A generation is never zero and never absent, so a delivery attempt always
    -- has a token to carry and a stale answer always has one to fail against.
    CONSTRAINT push_subscriptions_generation_check
        CHECK (generation >= 1),
    -- Bounded in bytes, not characters: the Go validator bounds len() and a
    -- btree entry is bytes, so a 2048-character multi-byte endpoint would
    -- otherwise pass validation and fail on the unique index instead.
    CONSTRAINT push_subscriptions_device_id_check
        CHECK (octet_length(device_id) BETWEEN 1 AND 128),
    CONSTRAINT push_subscriptions_endpoint_check
        CHECK (octet_length(endpoint) BETWEEN 1 AND 2048),
    CONSTRAINT push_subscriptions_p256dh_check
        CHECK (octet_length(p256dh) BETWEEN 1 AND 128),
    CONSTRAINT push_subscriptions_auth_check
        CHECK (octet_length(auth) BETWEEN 1 AND 128),
    -- A closed set, so the column can never carry a provider message. Nothing
    -- read from a push service is ever persisted here.
    CONSTRAINT push_subscriptions_invalidation_reason_check
        CHECK (invalidation_reason IS NULL
               OR invalidation_reason IN ('gone', 'not_found', 'user_disabled')),
    CONSTRAINT push_subscriptions_lifecycle_check
        CHECK (
            (status = 'active'
             AND invalidated_at IS NULL
             AND invalidation_reason IS NULL)
            OR (status <> 'active'
                AND invalidated_at IS NOT NULL
                AND invalidation_reason IS NOT NULL)
        )
);

-- The identity index. Also the read path: reconcile lists one (workspace, user)
-- and the future delivery pass reads the same prefix, so no separate index on
-- (workspace_id, user_id) is added — it would duplicate this one's prefix.
CREATE UNIQUE INDEX push_subscriptions_device_unique
    ON chat.push_subscriptions (workspace_id, user_id, device_id);

-- The ownership guard described above.
CREATE UNIQUE INDEX push_subscriptions_endpoint_unique
    ON chat.push_subscriptions (endpoint);

COMMIT;
