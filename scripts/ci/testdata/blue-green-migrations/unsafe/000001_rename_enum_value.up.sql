BEGIN;
ALTER TYPE chat.message_status RENAME VALUE 'sent' TO 'delivered';
COMMIT;
