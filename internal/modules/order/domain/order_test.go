//go:build examples

package domain_test

import (
	"testing"
	"time"

	"backend-go/internal/modules/order/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewOrderRecordsCreatedEvent(t *testing.T) {
	m, err := domain.NewMoney(500, "usd")
	require.NoError(t, err)
	o, err := domain.NewOrder("o1", " c1 ", m, time.Unix(0, 0))
	require.NoError(t, err)
	assert.Equal(t, "c1", o.CustomerID())
	assert.Equal(t, domain.StatusPaymentPending, o.Status())
	evs := o.PullEvents()
	require.Len(t, evs, 1)
	assert.Equal(t, "order.created", evs[0].EventName())
	assert.Empty(t, o.PullEvents(), "events are drained once")
}

func TestStateMachine(t *testing.T) {
	m, _ := domain.NewMoney(1, "IDR")
	o, _ := domain.NewOrder("o1", "c", m, time.Now())
	o.PullEvents()

	require.NoError(t, o.MarkPaid(time.Now()))
	assert.Len(t, o.PullEvents(), 1)
	require.NoError(t, o.MarkPaid(time.Now()), "paying twice is a no-op")
	assert.Empty(t, o.PullEvents())

	cancelled := domain.Rehydrate("o2", "c", m, domain.StatusCancelled, true, time.Now())
	assert.ErrorIs(t, cancelled.MarkPaid(time.Now()), domain.ErrInvalidStatus)
}

func TestMoneyValidation(t *testing.T) {
	for _, c := range []struct {
		minor int64
		cur   string
	}{{0, "IDR"}, {-5, "IDR"}, {5, "ID"}, {5, "I1R"}, {5, ""}} {
		_, err := domain.NewMoney(c.minor, c.cur)
		assert.ErrorIs(t, err, domain.ErrInvalidOrder, "%v", c)
	}
}
