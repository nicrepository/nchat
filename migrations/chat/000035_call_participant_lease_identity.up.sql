-- Issue #622 round 3: server-authoritative fencing token for resource-call
-- participant leases.
--
-- Problem: chat.call_participant_leases is keyed by (call_id, user_id) alone.
-- Two independent admissions of the SAME (call_id, user_id) — e.g. two
-- browser tabs, or one stale tab whose token/media request fails long after
-- a second, legitimate rejoin already superseded it — are indistinguishable
-- at the row level. A stale tab's own compensating call.leave (or a stale
-- call.presence heartbeat) can therefore delete or resurrect a DIFFERENT,
-- currently-live admission's lease purely because it shares the same
-- (call_id, user_id), even ending the call for everyone if that lease
-- happened to be the last one.
--
-- Fix: participation_id is an opaque, per-admission fencing token. Every
-- future CreateResourceCall/JoinResourceCall admission rotates it to a fresh,
-- unguessable UUID; call.leave/call.presence must supply the participation_id
-- their own admission received and can only ever affect a lease whose current
-- participation_id still matches. A stale value never matches once a newer
-- admission has rotated it — the row simply is not found by the fenced
-- predicate, and the operation becomes a safe, authorized no-op instead of a
-- destructive mutation.
--
-- Rollout (fail-closed, not "participation_id absent touches any lease"):
-- nullable initially. NULL means exclusively "a lease that predates this
-- migration/protocol" — existing rows become NULL when this migration runs.
-- The nullability is what
-- lets a legacy command (one that supplies no participation_id, from a
-- browser tab still running pre-Round-3 frontend code against an
-- already-upgraded chat-service) be handled without ambiguity: application
-- code treats an absent participation_id as "the caller claims the legacy
-- NULL identity", and that predicate (`participation_id IS NULL`) can never
-- match a lease a NEW, fenced admission just wrote (always non-NULL) — so an
-- old client can never mutate a new lease, only ever no-op against it. Every
-- new admission (after this deploy) always writes a non-NULL value, so the
-- legacy island empties out on its own as leases naturally expire/rotate and
-- never needs a backfill. A future migration MAY tighten this column to NOT
-- NULL once telemetry shows zero remaining NULL rows for long enough to be
-- confident no pre-Round-3 client is still connected — not part of this
-- migration.
--
-- No new index: the existing PRIMARY KEY (call_id, user_id) already locates
-- the row; participation_id is purely an additional fencing predicate over
-- that same row, not a new lookup path.

BEGIN;

ALTER TABLE chat.call_participant_leases
    ADD COLUMN participation_id UUID;

COMMIT;
