-- RF-23 rollback: remove only the call lifecycle table.

BEGIN;

DROP TABLE IF EXISTS chat.calls;

COMMIT;
