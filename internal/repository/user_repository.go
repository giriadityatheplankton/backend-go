package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"time"

	"backend-go/internal/domain"

	"github.com/nats-io/nats.go"
	"github.com/redis/go-redis/v9"
)

type userRepository struct {
	redisClient *redis.Client
	natsConn    *nats.Conn
}

// NewUserRepository creates a new instance of UserRepository.
func NewUserRepository(redisClient *redis.Client, natsConn *nats.Conn) domain.UserRepository {
	return &userRepository{
		redisClient: redisClient,
		natsConn:    natsConn,
	}
}

// GetByID returns a User by their ID.
func (r *userRepository) GetByID(id int) (*domain.User, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	cacheKey := fmt.Sprintf("user:%d", id)

	// 1. Caching (Redis) - check cache first
	if r.redisClient != nil {
		val, err := r.redisClient.Get(ctx, cacheKey).Result()
		if err == nil {
			var cachedUser domain.User
			if err := json.Unmarshal([]byte(val), &cachedUser); err == nil {
				log.Printf("[Redis] Cache Hit for user %d", id)
				r.publishAccessEvent(id, cachedUser.Name, "cache")
				return &cachedUser, nil
			}
		} else if err != redis.Nil {
			log.Printf("[Redis] Error fetching cache: %v", err)
		}
	}

	log.Printf("[Redis] Cache Miss for user %d", id)

	// 2. Database Fallback (Simulated)
	var user *domain.User
	if id == 1 {
		user = &domain.User{ID: 1, Name: "Developer", Email: "dev@example.com"}
	} else {
		return nil, errors.New("user not found")
	}

	// 3. Save to Redis cache
	if r.redisClient != nil {
		data, err := json.Marshal(user)
		if err == nil {
			err = r.redisClient.Set(ctx, cacheKey, data, 5*time.Minute).Err()
			if err != nil {
				log.Printf("[Redis] Failed to save cache: %v", err)
			} else {
				log.Printf("[Redis] Saved user %d to cache", id)
			}
		}
	}

	// 4. Pub/Sub (NATS) - publish access event
	r.publishAccessEvent(id, user.Name, "database")

	return user, nil
}

// Helper method to publish event to NATS
func (r *userRepository) publishAccessEvent(userID int, userName string, source string) {
	if r.natsConn == nil {
		return
	}

	event := map[string]interface{}{
		"user_id":     userID,
		"user_name":   userName,
		"accessed_at": time.Now().Format(time.RFC3339),
		"source":      source,
	}

	payload, err := json.Marshal(event)
	if err != nil {
		log.Printf("[NATS] Failed to marshal event: %v", err)
		return
	}

	subject := "user.accessed"
	err = r.natsConn.Publish(subject, payload)
	if err != nil {
		log.Printf("[NATS] Failed to publish message: %v", err)
	} else {
		log.Printf("[NATS] Event published to subject '%s'", subject)
	}
}
