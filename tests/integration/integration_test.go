//go:build integration
// +build integration

package integration_test

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"backend-go/internal/domain"
	"backend-go/internal/pkg/cache"
	"backend-go/internal/pkg/database"
	"backend-go/internal/pkg/database/migration"
	"backend-go/internal/pkg/outbox"
	"backend-go/internal/repository"
	"backend-go/internal/usecase"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	tcredis "github.com/testcontainers/testcontainers-go/modules/redis"
)

type mockIntegrationPublisher struct {
	publishedEvents []domain.UserAccessedEvent
}

func (m *mockIntegrationPublisher) PublishUserAccessed(ctx context.Context, event domain.UserAccessedEvent) error {
	m.publishedEvents = append(m.publishedEvents, event)
	return nil
}

func TestEndToEndWithTestcontainers(t *testing.T) {
	ctx := context.Background()

	// 1. Start PostgreSQL Container
	pgContainer, err := tcpostgres.Run(ctx,
		"postgres:16-alpine",
		tcpostgres.WithDatabase("testdb"),
		tcpostgres.WithUsername("testuser"),
		tcpostgres.WithPassword("testpass"),
		tcpostgres.BasicWaitStrategies(),
	)
	require.NoError(t, err)
	defer func() { _ = pgContainer.Terminate(ctx) }()

	connStr, err := pgContainer.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)

	// Connect to PostgreSQL
	db, err := sql.Open("postgres", connStr)
	require.NoError(t, err)
	defer db.Close()

	database.ConfigurePool(db, database.DefaultPoolConfig())

	// 2. Run Database Migrations
	migrator := migration.NewEngine()
	err = migrator.Run(ctx, db)
	require.NoError(t, err)

	// 3. Start Redis Container
	redisContainer, err := tcredis.Run(ctx,
		"redis:7-alpine",
	)
	require.NoError(t, err)
	defer func() { _ = redisContainer.Terminate(ctx) }()

	redisURI, err := redisContainer.ConnectionString(ctx)
	require.NoError(t, err)

	redisOpts, err := redis.ParseURL(redisURI)
	require.NoError(t, err)

	redisClient := redis.NewClient(redisOpts)
	defer redisClient.Close()

	// 4. Test Singleflight Cache with Real Redis
	cacheMgr := cache.NewManager(redisClient)
	cachedUser, err := cache.GetOrSet(ctx, cacheMgr, "user:1", 1*time.Minute, func(c context.Context) (*domain.User, error) {
		return &domain.User{ID: 1, Name: "Integration User", Email: "integration@example.com"}, nil
	})
	require.NoError(t, err)
	assert.Equal(t, "Integration User", cachedUser.Name)

	// 5. Test Usecase with Real Redis-backed Repository
	userRepo := repository.NewUserRepository(redisClient, 5*time.Minute)
	pub := &mockIntegrationPublisher{}
	uc := usecase.NewUserUsecase(userRepo, pub)

	user, err := uc.GetUser(ctx, 1)
	require.NoError(t, err)
	assert.Equal(t, 1, user.ID)

	// 6. Test Outbox Worker with Real Redis Distributed Lock
	outboxRepo := outbox.NewMemoryRepository()
	outboxLock := outbox.NewRedisDistributedLock(redisClient)

	// Acquire and release distributed lock
	token, err := outboxLock.Acquire(ctx, "outbox:lock:partition:0", 5*time.Second)
	assert.NoError(t, err)
	assert.NotEmpty(t, token)

	err = outboxLock.Release(ctx, "outbox:lock:partition:0", token)
	assert.NoError(t, err)
}
