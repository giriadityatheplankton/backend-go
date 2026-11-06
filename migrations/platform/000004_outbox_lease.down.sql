DROP INDEX IF EXISTS idx_outbox_published_updated;
DROP INDEX IF EXISTS idx_outbox_open_by_aggregate;
DROP INDEX IF EXISTS idx_outbox_claim;
ALTER TABLE outbox_events DROP COLUMN IF EXISTS locked_until, DROP COLUMN IF EXISTS locked_by;
