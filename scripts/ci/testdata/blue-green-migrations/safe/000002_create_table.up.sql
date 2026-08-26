BEGIN;
CREATE TABLE chat.reactions (
    id UUID PRIMARY KEY,
    message_id UUID NOT NULL REFERENCES chat.messages(id)
);
COMMIT;
