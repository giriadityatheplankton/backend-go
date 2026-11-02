//go:build examples

// Package redis holds cache decorators for the order repository.
package redis

import (
	"context"
	"time"

	"backend-go/internal/modules/order/domain"
	"backend-go/internal/modules/order/usecase/port"
	"backend-go/internal/platform/cache"
)

var _ port.OrderRepository = (*CachedOrderRepository)(nil)

// CachedOrderRepository decorates another OrderRepository with a read-through
// cache (open/closed: caching is added without touching the Postgres adapter or
// the use cases). Strong and ForUpdate reads bypass the cache.
type CachedOrderRepository struct {
	port.OrderRepository
	cache *cache.Manager
	ttl   time.Duration
}

func NewCachedOrderRepository(next port.OrderRepository, m *cache.Manager, ttl time.Duration) *CachedOrderRepository {
	if ttl <= 0 {
		ttl = 5 * time.Minute
	}
	return &CachedOrderRepository{OrderRepository: next, cache: m, ttl: ttl}
}

// orderDTO is the cache representation; the entity itself carries no tags.
type orderDTO struct {
	ID          string    `json:"id"`
	CustomerID  string    `json:"customer_id"`
	AmountMinor int64     `json:"amount_minor"`
	Currency    string    `json:"currency"`
	Status      string    `json:"status"`
	Enqueued    bool      `json:"enqueued"`
	CreatedAt   time.Time `json:"created_at"`
}

func key(id string) string { return cache.FormatKey("order", id) }

func (r *CachedOrderRepository) GetByID(ctx context.Context, id string, c port.Consistency) (*domain.Order, error) {
	if c != port.EventuallyConsistent {
		return r.OrderRepository.GetByID(ctx, id, c)
	}
	dto, err := cache.GetOrSet(ctx, r.cache, key(id), r.ttl, func(ctx context.Context) (orderDTO, error) {
		o, err := r.OrderRepository.GetByID(ctx, id, c)
		if err != nil {
			return orderDTO{}, err
		}
		return orderDTO{ID: o.ID(), CustomerID: o.CustomerID(), AmountMinor: o.Amount().Minor(), Currency: o.Amount().Currency(),
			Status: string(o.Status()), Enqueued: o.OutboxEnqueued(), CreatedAt: o.CreatedAt()}, nil
	})
	if err != nil {
		return nil, err
	}
	amount, err := domain.NewMoney(dto.AmountMinor, dto.Currency)
	if err != nil {
		return nil, err
	}
	return domain.Rehydrate(dto.ID, dto.CustomerID, amount, domain.Status(dto.Status), dto.Enqueued, dto.CreatedAt), nil
}

// Update invalidates after the write. A concurrent reader may briefly re-cache the
// old value; the TTL bounds that, and decisions use Strong reads anyway.
func (r *CachedOrderRepository) Update(ctx context.Context, o *domain.Order) error {
	if err := r.OrderRepository.Update(ctx, o); err != nil {
		return err
	}
	_ = r.cache.Invalidate(ctx, key(o.ID()))
	return nil
}
