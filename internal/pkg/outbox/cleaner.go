package outbox

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// CleanerConfig holds parameters for outbox retention cleaner.
type CleanerConfig struct {
	Interval        time.Duration
	RetentionPeriod time.Duration
	BatchSize       int
	LockTTL         time.Duration
}

// DefaultCleanerConfig provides production defaults for retention cleanup.
func DefaultCleanerConfig() CleanerConfig {
	return CleanerConfig{
		Interval:        10 * time.Minute,
		RetentionPeriod: 7 * 24 * time.Hour,
		BatchSize:       500,
		LockTTL:         30 * time.Second,
	}
}

// Cleaner periodically purges published events older than retention period to prevent table bloat.
type Cleaner struct {
	cfg    CleanerConfig
	repo   Repository
	lock   DistributedLock
	stopCh chan struct{}
	wg     sync.WaitGroup
}

// NewCleaner initializes outbox retention cleaner.
func NewCleaner(cfg CleanerConfig, repo Repository, lock DistributedLock) *Cleaner {
	if cfg.Interval <= 0 {
		cfg.Interval = 10 * time.Minute
	}
	if cfg.RetentionPeriod <= 0 {
		cfg.RetentionPeriod = 7 * 24 * time.Hour
	}
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = 500
	}
	if cfg.LockTTL <= 0 {
		cfg.LockTTL = 30 * time.Second
	}

	return &Cleaner{
		cfg:    cfg,
		repo:   repo,
		lock:   lock,
		stopCh: make(chan struct{}),
	}
}

// Start launches the periodic cleaner background goroutine.
func (c *Cleaner) Start(ctx context.Context) {
	c.wg.Add(1)
	go c.runLoop(ctx)
}

// Stop gracefully terminates the cleaner.
func (c *Cleaner) Stop() {
	close(c.stopCh)
	c.wg.Wait()
}

func (c *Cleaner) runLoop(ctx context.Context) {
	defer c.wg.Done()

	ticker := time.NewTicker(c.cfg.Interval)
	defer ticker.Stop()

	// Initial run after short startup delay
	select {
	case <-time.After(5 * time.Second):
		c.purgeBatch(ctx)
	case <-ctx.Done():
		return
	case <-c.stopCh:
		return
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-c.stopCh:
			return
		case <-ticker.C:
			c.purgeBatch(ctx)
		}
	}
}

func (c *Cleaner) purgeBatch(ctx context.Context) {
	lockKey := "outbox:lock:cleaner"

	// Acquire distributed lock so only one replica performs DB deletion
	if c.lock != nil {
		token, err := c.lock.Acquire(ctx, lockKey, c.cfg.LockTTL)
		if err != nil {
			return // Another pod is running retention cleanup
		}
		defer func() {
			_ = c.lock.Release(ctx, lockKey, token)
		}()
	}

	cutoff := time.Now().Add(-c.cfg.RetentionPeriod)
	deleted, err := c.repo.DeletePublishedBefore(ctx, cutoff, c.cfg.BatchSize)
	if err != nil {
		slog.ErrorContext(ctx, "Failed to purge expired outbox records", "error", err)
		return
	}

	if deleted > 0 {
		slog.InfoContext(ctx, "Purged expired outbox records", "deleted_count", deleted, "cutoff", cutoff)
	}
}
