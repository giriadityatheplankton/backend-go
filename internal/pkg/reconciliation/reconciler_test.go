package reconciliation_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"backend-go/internal/pkg/reconciliation"

	"github.com/stretchr/testify/assert"
)

type mockReconciliationJob struct {
	reconciledCount int32
}

func (m *mockReconciliationJob) Name() string {
	return "pending-orders-job"
}

func (m *mockReconciliationJob) FetchStaleRecords(ctx context.Context, threshold time.Duration, limit int) ([]reconciliation.StaleRecord, error) {
	return []reconciliation.StaleRecord{
		{ID: "ord-1", Reference: "ref-1", Status: "PENDING"},
		{ID: "ord-2", Reference: "ref-2", Status: "PENDING"},
	}, nil
}

func (m *mockReconciliationJob) Reconcile(ctx context.Context, record reconciliation.StaleRecord) (reconciliation.Outcome, error) {
	atomic.AddInt32(&m.reconciledCount, 1)
	return reconciliation.OutcomeResolved, nil
}

func TestReconciliationEngine(t *testing.T) {
	job := &mockReconciliationJob{}
	cfg := reconciliation.Config{
		Interval:  10 * time.Millisecond,
		Threshold: 1 * time.Minute,
		BatchSize: 10,
	}

	engine := reconciliation.NewEngine(cfg, nil)
	engine.RegisterJob(job)

	ctx, cancel := context.WithCancel(context.Background())
	engine.Start(ctx)

	time.Sleep(50 * time.Millisecond)
	cancel()
	engine.Stop()

	assert.GreaterOrEqual(t, atomic.LoadInt32(&job.reconciledCount), int32(2))
}
