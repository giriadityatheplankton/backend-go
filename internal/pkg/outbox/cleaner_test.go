package outbox_test

import (
	"context"
	"testing"
	"time"

	"backend-go/internal/pkg/outbox"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPurgerDeletesOnlyOldPublished(t *testing.T) {
	repo := outbox.NewMemoryRepository()
	ctx := context.Background()
	for _, id := range []string{"old", "new"} {
		_ = repo.Save(ctx, ev(id, id, time.Now()))
	}
	claimed, _ := repo.Claim(ctx, 0, 10, "w", time.Minute, false)
	require.Len(t, claimed, 2)
	for _, e := range claimed {
		require.NoError(t, repo.MarkPublished(ctx, e.ID, "w"))
	}
	time.Sleep(10 * time.Millisecond)
	// Only "old" is older than the cutoff.
	n, err := repo.DeletePublishedBefore(ctx, time.Now().Add(time.Hour), 1)
	require.NoError(t, err)
	assert.Equal(t, int64(1), n, "limit is honoured")
}
