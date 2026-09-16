BEGIN;

-- Which endpoints a notification has already reached (issue #746).
--
-- # Why this table has to exist
--
-- A notification fans out to every browser one person has registered, and the
-- attempts do not all end the same way. One endpoint answering 5xx keeps the
-- outbox row retryable; the endpoint that answered 2xx in the same pass must
-- not be sent to again when that retry comes round. Nothing already persisted
-- can say that. chat.push_subscriptions.last_success_at is per subscription and
-- not per notification, and the outbox row is per notification and not per
-- subscription. This is the intersection, and it is the smallest thing that
-- closes the requirement.
--
-- # Why only successes are recorded
--
-- A permanent failure is already derivable: the push service's 404 or 410
-- retires the subscription (000045), and a retired subscription is not in the
-- fan-out. A transient failure must be retried, so recording it would change
-- nothing. What is left is exactly the fact this table holds — "this endpoint
-- already got it" — one row, written once.
--
-- # Identity
--
-- (notification_id, subscription_id) is the primary key, and it is the whole
-- deduplication. A replayed attempt inserts nothing (ON CONFLICT DO NOTHING),
-- so the table cannot grow with retries, and two workers racing on one claim
-- cannot produce two logical deliveries even though only one of them holds it.
--
-- generation is recorded but is deliberately not part of the key. A browser
-- that re-subscribes gets a new endpoint, not a new person: somebody who has
-- already been told about a message must not be told again because their
-- browser rotated its subscription. The column is here so an operator can tell
-- which endpoint lifetime actually received it.
--
-- # Growth
--
-- Both foreign keys cascade. The outbox retention documented in
-- notification-outbox.md removes terminal rows after thirty days and takes
-- these with them; deleting a subscription takes its rows too. So this table is
-- bounded by the outbox rather than by anything of its own, and needs no
-- retention policy that could disagree with the outbox's.
CREATE TABLE chat.notification_push_deliveries (
    notification_id UUID        NOT NULL
        REFERENCES chat.notification_outbox (id) ON DELETE CASCADE,
    subscription_id UUID        NOT NULL
        REFERENCES chat.push_subscriptions (id) ON DELETE CASCADE,
    generation      BIGINT      NOT NULL,
    delivered_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (notification_id, subscription_id),
    CONSTRAINT notification_push_deliveries_generation_check
        CHECK (generation >= 1)
);

-- The fan-out asks "which of this recipient's active subscriptions has this
-- notification not reached yet?", as a NOT EXISTS anchored on the primary key
-- above. Deleting a subscription needs the other direction, and a foreign key
-- with ON DELETE CASCADE has no index of its own, so a delete would otherwise
-- sequentially scan this table once per subscription removed.
CREATE INDEX idx_notification_push_deliveries_subscription
    ON chat.notification_push_deliveries (subscription_id);

COMMIT;
