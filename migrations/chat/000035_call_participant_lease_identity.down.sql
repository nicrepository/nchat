BEGIN;

ALTER TABLE chat.call_participant_leases
    DROP COLUMN participation_id;

COMMIT;
