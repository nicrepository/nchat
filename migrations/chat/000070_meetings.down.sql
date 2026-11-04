-- The two tables reference each other, so they are dropped in one statement.
BEGIN;
DROP TABLE IF EXISTS chat.meetings, chat.meeting_participants;
COMMIT;
