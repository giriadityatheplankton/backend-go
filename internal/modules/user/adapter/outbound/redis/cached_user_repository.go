//go:build examples

// Package redis caches user reads.
package redis

import (
	"context"
	"strconv"
	"time"

	"backend-go/internal/modules/user/domain"
	"backend-go/internal/modules/user/usecase/port"
	"backend-go/internal/platform/cache"
)

var _ port.UserRepository = (*CachedUserRepository)(nil)

// CachedUserRepository is a read-through decorator around any UserRepository.
type CachedUserRepository struct {
	next  port.UserRepository
	cache *cache.Manager
	ttl   time.Duration
}

func NewCachedUserRepository(next port.UserRepository, m *cache.Manager, ttl time.Duration) *CachedUserRepository {
	return &CachedUserRepository{next: next, cache: m, ttl: ttl}
}

type userDTO struct {
	ID    int    `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email"`
}

func (r *CachedUserRepository) GetByID(ctx context.Context, id int) (*domain.User, error) {
	dto, err := cache.GetOrSet(ctx, r.cache, cache.FormatKey("user", strconv.Itoa(id)), r.ttl, func(ctx context.Context) (userDTO, error) {
		u, err := r.next.GetByID(ctx, id)
		if err != nil {
			return userDTO{}, err
		}
		return userDTO{ID: u.ID, Name: u.Name, Email: u.Email}, nil
	})
	if err != nil {
		return nil, err
	}
	return &domain.User{ID: dto.ID, Name: dto.Name, Email: dto.Email}, nil
}
