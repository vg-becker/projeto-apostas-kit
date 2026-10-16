CREATE TABLE inbox_messages (
    consumer_name text        NOT NULL,
    message_id    text        NOT NULL,
    payload_hash  char(64)    NOT NULL,
    received_at   timestamptz NOT NULL,
    completed_at  timestamptz,
    PRIMARY KEY (consumer_name, message_id),
    CONSTRAINT inbox_messages_payload_hash_format
        CHECK (payload_hash ~ '^[0-9a-f]{64}$'),
    CONSTRAINT inbox_messages_completed_not_before_received
        CHECK (completed_at IS NULL OR completed_at >= received_at)
);
