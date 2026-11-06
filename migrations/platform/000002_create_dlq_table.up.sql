CREATE TABLE IF NOT EXISTS dlq_events (
    id VARCHAR(64) PRIMARY KEY,
    original_event_id VARCHAR(64) NOT NULL,
    event_type VARCHAR(150) NOT NULL,
    payload BYTEA NOT NULL,
    headers JSONB NOT NULL DEFAULT '{}',
    failure_reason TEXT NOT NULL,
    retry_attempts INT NOT NULL,
    failed_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_dlq_failed_at ON dlq_events (failed_at);
