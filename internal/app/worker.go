package app

import (
	"context"
	"log/slog"
	"os"
	"time"

	"backend-go/internal/platform/messaging"
	"backend-go/internal/platform/messaging/inbox"
	"backend-go/internal/platform/messaging/redisstream"
	"backend-go/internal/platform/outbox"
	"backend-go/internal/platform/reconciliation"
	"backend-go/internal/platform/resilience"

	"github.com/sony/gobreaker/v2"
)

// runner is a long-running background component started by buildWorkers.
type runner struct {
	name string
	run  func(ctx context.Context) error
}

// buildWorkers wires the outbox relay (-> Redis Streams), the stream consumers
// (inbox-protected), the cleaners and the reconciliation jobs. It returns nothing
// to run when the dependencies they need are not configured.
func (a *App) buildWorkers() []runner {
	if a.infra.db == nil || a.infra.redisState == nil {
		slog.Warn("Background workers disabled: they require PostgreSQL and Redis")
		return nil
	}
	var rs []runner
	cfg, in := a.cfg, a.infra
	repo := outbox.NewPostgresRepository(in.db.Write())
	lock := outbox.NewRedisDistributedLock(in.redisState, cfg.ServiceName+":lock:")

	// Producer side: outbox -> Redis Streams, guarded by a circuit breaker so a
	// Redis outage fails fast and recovers via half-open probes instead of piling up timeouts.
	failures := uint32(cfg.BreakerFailures) //nolint:gosec // validated > 0 in Config.Validate
	breaker := resilience.NewCircuitBreaker(resilience.CircuitBreakerConfig{
		Name: "outbox-publisher", ConsecutiveFailures: failures, Timeout: cfg.BreakerTimeout,
		OnStateChange: func(name string, from, to gobreaker.State) {
			in.metrics.SetBreakerState(name, resilience.StateValue(to))
			slog.Warn("Circuit breaker state changed", "name", name, "from", from.String(), "to", to.String())
		},
	})
	publisher := messaging.GuardedPublisher(
		redisstream.NewPublisher(in.redisState, redisstream.PublisherConfig{Prefix: cfg.StreamPrefix, MaxLen: cfg.StreamMaxLen}), breaker)

	host, _ := os.Hostname()
	wcfg := outbox.DefaultConfig()
	wcfg.PartitionCount, wcfg.BatchSize, wcfg.PollInterval = cfg.OutboxPartitions, cfg.OutboxBatchSize, cfg.OutboxPollInterval
	wcfg.MaxRetries, wcfg.LeaseDuration, wcfg.PreserveOrder = cfg.OutboxMaxRetries, cfg.OutboxLease, cfg.OutboxPreserveOrder
	wcfg.Owner = host + "-" + time.Now().UTC().Format("150405.000")
	relay := outbox.NewWorker(wcfg, repo, publisher, outbox.ByAggregateType(""), in.metrics)
	rs = append(rs, runner{"outbox-relay", relay.Run})

	ccfg := outbox.DefaultCleanerConfig()
	ccfg.RetentionPeriod = cfg.OutboxRetention
	rs = append(rs, runner{"outbox-cleaner", outbox.NewCleaner(ccfg, repo, lock).Run})

	// Reconciliation jobs contributed by modules.
	var jobs []reconciliation.Job
	for _, m := range a.modules {
		jobs = append(jobs, m.Jobs()...)
	}
	if len(jobs) > 0 {
		rcfg := reconciliation.DefaultConfig()
		rcfg.Interval, rcfg.Threshold = cfg.ReconcileInterval, cfg.ReconcileThreshold
		eng := reconciliation.NewEngine(rcfg, lock)
		for _, j := range jobs {
			eng.RegisterJob(j)
		}
		rs = append(rs, runner{"reconciliation", eng.Run})
	}

	// Consumer side: every subscription runs through the inbox (layer 3) so a
	// redelivered message is applied once; the broker ack happens after COMMIT.
	for _, m := range a.modules {
		for _, sub := range m.Subscriptions() {
			ib := inbox.New(in.tx, in.db.Write(), sub.Name, in.metrics)
			c, err := redisstream.NewConsumer(in.redisState, redisstream.ConsumerConfig{
				Stream: redisstream.StreamName(cfg.StreamPrefix, sub.Stream), Group: cfg.ConsumerGroup, Name: host,
				Workers: cfg.ConsumerWorkers, BatchSize: cfg.ConsumerBatchSize,
				MaxDeliveries: cfg.ConsumerMaxDeliveries, ClaimIdle: cfg.ConsumerClaimIdle,
			}, ib.Wrap(sub.Handler), in.metrics)
			if err != nil {
				slog.Error("Invalid consumer configuration", "subscription", sub.Name, "error", err)
				continue
			}
			rs = append(rs, runner{"consumer:" + sub.Name, c.Run})
		}
	}
	if len(rs) > 0 {
		purge := inbox.New(in.tx, in.db.Write(), "purge", nil)
		rs = append(rs, runner{"inbox-purge", func(ctx context.Context) error {
			t := time.NewTicker(time.Hour)
			defer t.Stop()
			for {
				select {
				case <-ctx.Done():
					return nil
				case <-t.C:
					if n, err := purge.Purge(ctx, time.Now().Add(-cfg.InboxRetention), 5000); err != nil {
						slog.Warn("Inbox purge failed", "error", err)
					} else if n > 0 {
						slog.Info("Inbox purged", "rows", n)
					}
				}
			}
		}})
	}
	return rs
}
