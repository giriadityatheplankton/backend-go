-- Consumer-side de-duplication (layer 3). PK makes "first time seen" atomic.
CREATE TABLE IF NOT EXISTS inbox_messages (
    consumer    VARCHAR(100) NOT NULL,
    message_id  VARCHAR(128) NOT NULL,
    received_at TIMESTAMPTZ  NOT NULL DEFAULT now(),
    PRIMARY KEY (consumer, message_id)
);

CREATE INDEX IF NOT EXISTS idx_inbox_received_at ON inbox_messages (received_at);
