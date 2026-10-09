package outbox_test

import (
	"context"
	"testing"
	"time"

	"backend-go/internal/pkg/outbox"

	"github.com/stretchr/testify/assert"
)

func TestCleanerPurge(t *testing.T) {
	repo := outbox.NewMemoryRepository()
	lock := &mockDistributedLock{}

	// Insert old published event
	oldEvent := &outbox.OutboxEvent{
		ID:        "old-1",
		Status:    outbox.StatusPublished,
		UpdatedAt: time.Now().Add(-10 * 24 * time.Hour),
	}
	_ = repo.Save(context.Background(), oldEvent)

	// Insert fresh published event
	newEvent := &outbox.OutboxEvent{
		ID:        "new-1",
		Status:    outbox.StatusPublished,
		UpdatedAt: time.Now(),
	}
	_ = repo.Save(context.Background(), newEvent)

	deleted, err := repo.DeletePublishedBefore(context.Background(), time.Now().Add(-7*24*time.Hour), 100)
	assert.NoError(t, err)
	assert.Equal(t, int64(1), deleted)

	cfg := outbox.CleanerConfig{
		Interval:        10 * time.Millisecond,
		RetentionPeriod: 7 * 24 * time.Hour,
		BatchSize:       10,
		LockTTL:         100 * time.Millisecond,
	}
	cleaner := outbox.NewCleaner(cfg, repo, lock)
	assert.NotNil(t, cleaner)
}
