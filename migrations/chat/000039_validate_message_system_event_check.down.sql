BEGIN;

-- Validation cannot be undone in place; the constraint returns to NOT VALID by
-- being dropped and re-added, which is what 000038's down does.
SELECT 1;

COMMIT;
