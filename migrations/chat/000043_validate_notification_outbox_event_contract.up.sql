-- 000043_validate_notification_outbox_event_contract.up.sql
-- Issue #741: the scanning half of 000042's CHECK constraints.
--
-- 000042 added them NOT VALID, which is a catalogue write: every insert and
-- update is checked from the instant it commits, but the rows already stored are
-- not read. This migration performs that read and marks the constraints
-- validated, so the planner may rely on them.
--
-- Lock taken here: SHARE UPDATE EXCLUSIVE. It conflicts with DDL and with VACUUM
-- FULL, and with nothing an application does — producing notifications and
-- reading them both proceed while it runs.
--
-- The scan cannot fail. kind and status were widened to strict supersets of the
-- sets every stored row was written under, and the four constraints over the new
-- columns are satisfied by the defaults and the backfill 000042 applied in the
-- same transaction that created them.

BEGIN;

ALTER TABLE chat.notification_outbox
    VALIDATE CONSTRAINT notification_outbox_kind_check;

ALTER TABLE chat.notification_outbox
    VALIDATE CONSTRAINT notification_outbox_status_check;

ALTER TABLE chat.notification_outbox
    VALIDATE CONSTRAINT notification_outbox_source_type_check;

ALTER TABLE chat.notification_outbox
    VALIDATE CONSTRAINT notification_outbox_priority_check;

ALTER TABLE chat.notification_outbox
    VALIDATE CONSTRAINT notification_outbox_origin_check;

ALTER TABLE chat.notification_outbox
    VALIDATE CONSTRAINT notification_outbox_suppressed_reason_check;

ALTER TABLE chat.notification_outbox
    VALIDATE CONSTRAINT notification_outbox_dedupe_key_check;

COMMIT;
