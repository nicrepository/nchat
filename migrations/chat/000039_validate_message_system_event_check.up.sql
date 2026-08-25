BEGIN;

-- Promote the system-event shape check to validated, the same two-step the
-- link-safety state constraint uses (000029/000030): 000038 adds it NOT VALID so
-- the deploy never blocks on a table scan, and this pass proves every existing
-- row satisfies it and makes the planner trust it.
ALTER TABLE chat.messages VALIDATE CONSTRAINT messages_system_event_check;

COMMIT;
