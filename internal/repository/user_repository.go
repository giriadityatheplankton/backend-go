package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"backend-go/internal/domain"

	"github.com/redis/go-redis/v9"
)

type userRepository struct {
	redisClient *redis.Client
	cacheTTL    time.Duration
}

// NewUserRepository creates a new instance of domain.UserRepository.
func NewUserRepository(redisClient *redis.Client, cacheTTL time.Duration) domain.UserRepository {
	if cacheTTL == 0 {
		cacheTTL = 5 * time.Minute
	}
	return &userRepository{
		redisClient: redisClient,
		cacheTTL:    cacheTTL,
	}
}

// GetByID returns a User by ID with Redis caching.
func (r *userRepository) GetByID(ctx context.Context, id int) (*domain.User, error) {
	cacheKey := fmt.Sprintf("user:%d", id)

	// 1. Try fetching from Redis Cache
	if r.redisClient != nil {
		val, err := r.redisClient.Get(ctx, cacheKey).Result()
		if err == nil {
			var cachedUser domain.User
			if err := json.Unmarshal([]byte(val), &cachedUser); err == nil {
				slog.Info("Cache HIT", "user_id", id, "key", cacheKey)
				return &cachedUser, nil
			}
		} else if err != redis.Nil {
			slog.Warn("Redis cache fetch failed, falling back to database", "user_id", id, "error", err)
		}
	}

	slog.Info("Cache MISS", "user_id", id)

	// 2. Fetch from Database (Simulated persistence)
	user, err := r.fetchFromDatabase(ctx, id)
	if err != nil {
		return nil, err
	}

	// 3. Save result asynchronously to Redis cache
	if r.redisClient != nil {
		data, err := json.Marshal(user)
		if err == nil {
			if setErr := r.redisClient.Set(ctx, cacheKey, data, r.cacheTTL).Err(); setErr != nil {
				slog.Warn("Failed to save user to cache", "user_id", id, "error", setErr)
			} else {
				slog.Debug("Saved user to Redis cache", "user_id", id, "ttl", r.cacheTTL)
			}
		}
	}

	return user, nil
}

func (r *userRepository) fetchFromDatabase(ctx context.Context, id int) (*domain.User, error) {
	// Check for context cancellation before executing DB operation
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	// Simulated DB logic
	if id == 1 {
		return &domain.User{
			ID:    1,
			Name:  "Developer",
			Email: "dev@example.com",
		}, nil
	}

	return nil, domain.ErrUserNotFound
}
