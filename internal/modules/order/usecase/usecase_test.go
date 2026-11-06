//go:build examples

package usecase_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"backend-go/internal/kernel/clock"
	"backend-go/internal/kernel/ids"
	"backend-go/internal/modules/order/domain"
	"backend-go/internal/modules/order/usecase"
	"backend-go/internal/modules/order/usecase/port"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---- in-memory fakes: the use cases run with no Docker, DB or Redis ----

type fakeRepo struct {
	mu             sync.Mutex
	orders         map[string]*domain.Order
	keys           map[string]string // idempotency key -> order id
	fps            map[string]string
	failCreateWith error
}

func newRepo() *fakeRepo {
	return &fakeRepo{orders: map[string]*domain.Order{}, keys: map[string]string{}, fps: map[string]string{}}
}

func (r *fakeRepo) Create(_ context.Context, o *domain.Order, key, fp string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failCreateWith != nil {
		return r.failCreateWith
	}
	if _, dup := r.keys[key]; dup {
		return port.ErrDuplicateKey
	}
	r.orders[o.ID()], r.keys[key], r.fps[key] = o, o.ID(), fp
	return nil
}
func (r *fakeRepo) FindByIdempotencyKey(_ context.Context, key string) (*domain.Order, string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	id, ok := r.keys[key]
	if !ok {
		return nil, "", domain.ErrOrderNotFound
	}
	return r.orders[id], r.fps[key], nil
}
func (r *fakeRepo) GetByID(_ context.Context, id string, _ port.Consistency) (*domain.Order, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if o, ok := r.orders[id]; ok {
		return o, nil
	}
	return nil, domain.ErrOrderNotFound
}
func (r *fakeRepo) Update(context.Context, *domain.Order) error { return nil }
func (r *fakeRepo) FindWithoutOutbox(_ context.Context, cutoff time.Time, limit int) ([]*domain.Order, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []*domain.Order
	for _, o := range r.orders {
		if !o.OutboxEnqueued() && o.CreatedAt().Before(cutoff) {
			out = append(out, o)
		}
	}
	return out, nil
}
func (r *fakeRepo) MarkOutboxEnqueued(_ context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	o := r.orders[id]
	r.orders[id] = domain.Rehydrate(o.ID(), o.CustomerID(), o.Amount(), o.Status(), true, o.CreatedAt())
	return nil
}

type fakeOutbox struct {
	mu     sync.Mutex
	events []domain.Event
}

func (o *fakeOutbox) Enqueue(_ context.Context, ev ...domain.Event) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.events = append(o.events, ev...)
	return nil
}

type passTx struct{ calls int32 }

func (t *passTx) WithinTx(ctx context.Context, fn func(context.Context) error) error {
	atomic.AddInt32(&t.calls, 1)
	return fn(ctx)
}

func newCreate(t *testing.T) (*usecase.CreateOrder, *fakeRepo, *fakeOutbox) {
	repo, ob := newRepo(), &fakeOutbox{}
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	return usecase.NewCreateOrder(&passTx{}, repo, ob, &ids.Sequence{}, clock.Fixed{T: now}), repo, ob
}

func in(key string) usecase.CreateOrderInput {
	return usecase.CreateOrderInput{IdempotencyKey: key, CustomerID: "c1", AmountMinor: 1500, Currency: "idr"}
}

func TestCreateOrder_HappyPathEmitsOneEvent(t *testing.T) {
	uc, _, ob := newCreate(t)
	out, err := uc.Execute(context.Background(), in("k1"))
	require.NoError(t, err)
	assert.Equal(t, "id-1", out.ID)
	assert.Equal(t, "PAYMENT_PENDING", out.Status)
	assert.Equal(t, "IDR", out.Currency, "currency is normalised")
	assert.False(t, out.Replayed)
	require.Len(t, ob.events, 1)
	assert.Equal(t, "order.created", ob.events[0].EventName())
}

func TestCreateOrder_SameKeySamePayloadReplaysWithoutSecondOrder(t *testing.T) {
	uc, repo, ob := newCreate(t)
	first, err := uc.Execute(context.Background(), in("k1"))
	require.NoError(t, err)
	second, err := uc.Execute(context.Background(), in("k1"))
	require.NoError(t, err)

	assert.Equal(t, first.ID, second.ID)
	assert.True(t, second.Replayed)
	assert.Len(t, repo.orders, 1)
	assert.Len(t, ob.events, 1, "a replay must not emit another event")
}

func TestCreateOrder_SameKeyDifferentPayloadIsRejected(t *testing.T) {
	uc, _, _ := newCreate(t)
	_, err := uc.Execute(context.Background(), in("k1"))
	require.NoError(t, err)
	other := in("k1")
	other.AmountMinor = 9999
	_, err = uc.Execute(context.Background(), other)
	assert.ErrorIs(t, err, domain.ErrIdempotencyKeyReused)
}

func TestCreateOrder_ConcurrentRequestsWithSameKeyCreateExactlyOne(t *testing.T) {
	uc, repo, ob := newCreate(t)
	var wg sync.WaitGroup
	results := make([]usecase.OrderOutput, 24)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			out, err := uc.Execute(context.Background(), in("race"))
			assert.NoError(t, err)
			results[i] = out
		}(i)
	}
	wg.Wait()

	assert.Len(t, repo.orders, 1, "the UNIQUE key (layer 2) is the source of truth")
	assert.Len(t, ob.events, 1)
	for _, r := range results {
		assert.Equal(t, results[0].ID, r.ID)
	}
}

func TestCreateOrder_ValidationErrors(t *testing.T) {
	uc, _, _ := newCreate(t)
	cases := map[string]usecase.CreateOrderInput{
		"no key":       {CustomerID: "c", AmountMinor: 1, Currency: "IDR"},
		"long key":     {IdempotencyKey: string(make([]byte, 129)), CustomerID: "c", AmountMinor: 1, Currency: "IDR"},
		"zero amount":  {IdempotencyKey: "k", CustomerID: "c", AmountMinor: 0, Currency: "IDR"},
		"bad currency": {IdempotencyKey: "k", CustomerID: "c", AmountMinor: 1, Currency: "ID1"},
		"empty cust":   {IdempotencyKey: "k", CustomerID: " ", AmountMinor: 1, Currency: "IDR"},
	}
	for name, input := range cases {
		_, err := uc.Execute(context.Background(), input)
		assert.Error(t, err, name)
	}
}

func TestCreateOrder_InfrastructureErrorIsNotSwallowed(t *testing.T) {
	uc, repo, ob := newCreate(t)
	repo.failCreateWith = errors.New("connection reset")
	_, err := uc.Execute(context.Background(), in("k"))
	require.Error(t, err)
	assert.Empty(t, ob.events)
}

func TestMarkOrderPaid_IsIdempotent(t *testing.T) {
	create, repo, ob := newCreate(t)
	out, _ := create.Execute(context.Background(), in("k"))
	paid := usecase.NewMarkOrderPaid(&passTx{}, repo, ob, clock.System{})

	require.NoError(t, paid.Execute(context.Background(), out.ID))
	require.NoError(t, paid.Execute(context.Background(), out.ID)) // redelivery
	names := []string{}
	for _, e := range ob.events {
		names = append(names, e.EventName())
	}
	assert.Equal(t, []string{"order.created", "order.paid"}, names, "order.paid is emitted once")
	assert.ErrorIs(t, paid.Execute(context.Background(), "missing"), domain.ErrOrderNotFound)
}

func TestReconcileOutbox_RestoresMissingEvent(t *testing.T) {
	repo, ob := newRepo(), &fakeOutbox{}
	old := time.Now().Add(-time.Hour)
	amount, _ := domain.NewMoney(100, "IDR")
	repo.orders["o1"] = domain.Rehydrate("o1", "c", amount, domain.StatusPaymentPending, false, old)
	rec := usecase.NewReconcileOutbox(&passTx{}, repo, ob, clock.System{})

	stale, err := rec.FindStale(context.Background(), 15*time.Minute, 10)
	require.NoError(t, err)
	require.Len(t, stale, 1)
	require.NoError(t, rec.Repair(context.Background(), "o1"))
	require.NoError(t, rec.Repair(context.Background(), "o1")) // second run is a no-op

	assert.Len(t, ob.events, 1)
	stale, _ = rec.FindStale(context.Background(), 15*time.Minute, 10)
	assert.Empty(t, stale)
}

func TestGetOrder_Validation(t *testing.T) {
	get := usecase.NewGetOrder(newRepo())
	_, err := get.Execute(context.Background(), usecase.GetOrderInput{ID: " "})
	assert.ErrorIs(t, err, domain.ErrInvalidOrder)
	_, err = get.Execute(context.Background(), usecase.GetOrderInput{ID: "nope"})
	assert.ErrorIs(t, err, domain.ErrOrderNotFound)
}
