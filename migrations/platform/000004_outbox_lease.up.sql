-- Postgres-side leasing for the outbox relay (no Redis lock needed).
ALTER TABLE outbox_events
    ADD COLUMN IF NOT EXISTS locked_by    VARCHAR(100),
    ADD COLUMN IF NOT EXISTS locked_until TIMESTAMPTZ;

-- Hot path: partition scan for due work and expired leases.
CREATE INDEX IF NOT EXISTS idx_outbox_claim
    ON outbox_events (partition, created_at, id)
    WHERE status IN ('PENDING', 'FAILED', 'PROCESSING');

-- Per-aggregate ordering check and backlog stats.
CREATE INDEX IF NOT EXISTS idx_outbox_open_by_aggregate
    ON outbox_events (aggregate_id, created_at, id)
    WHERE status IN ('PENDING', 'FAILED', 'PROCESSING');

-- Cleaner scan.
CREATE INDEX IF NOT EXISTS idx_outbox_published_updated
    ON outbox_events (updated_at)
    WHERE status = 'PUBLISHED';
