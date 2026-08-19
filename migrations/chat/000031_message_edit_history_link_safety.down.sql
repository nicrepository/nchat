BEGIN;

DROP TABLE chat.message_edit_history_link_scans;

DROP TRIGGER messages_invalidate_legacy_link_projection ON chat.messages;
DROP FUNCTION chat.invalidate_legacy_message_link_projection();

ALTER TABLE chat.messages
    DROP COLUMN link_safety_projection_version;

ALTER TABLE chat.message_edit_history
    DROP COLUMN link_safety_fingerprint;

COMMIT;
