-- migrations/auth/000006_device_session_indexes.up.sql
-- Adds compound indexes to support efficient ordering and device-cascade revocation
-- for the device/session management endpoints (RF-51, RF-52, RF-53).
--
-- Existing indexes from 000001 NOT duplicated:
--   idx_user_sessions_user_revoked  (user_id, revoked_at)
--   idx_user_sessions_user_device   (user_id, device_id)
--   idx_user_devices_user_revoked   (user_id, revoked_at)
--
-- New indexes add the sort column and a partial device index with revoked_at.

BEGIN;

SET LOCAL search_path = auth, public;

-- List sessions newest-first per user (ORDER BY created_at DESC, id DESC).
CREATE INDEX idx_user_sessions_user_created
    ON auth.user_sessions (user_id, created_at DESC, id DESC);

-- Device revocation cascade: find active sessions by (user, device_id) with revoked_at filter.
-- Partial: excludes rows with NULL device_id (sessions not bound to any device).
-- Different from existing idx_user_sessions_user_device (user_id, device_id) — adds revoked_at.
CREATE INDEX idx_user_sessions_user_device_revoked
    ON auth.user_sessions (user_id, device_id, revoked_at)
    WHERE device_id IS NOT NULL;

-- List devices newest-first per user (ORDER BY last_seen_at DESC, id DESC).
CREATE INDEX idx_user_devices_user_last_seen
    ON auth.user_devices (user_id, last_seen_at DESC, id DESC);

COMMIT;
