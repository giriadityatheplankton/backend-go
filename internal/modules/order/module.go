//go:build examples

// Package order is the order bounded context. Everything about orders lives
// under this directory, so it can be removed or extracted into its own service
// by moving one folder.
package order

import (
	"time"

	orderv1 "backend-go/gen/proto/order/v1"
	"backend-go/internal/kernel/clock"
	"backend-go/internal/kernel/ids"
	consumerin "backend-go/internal/modules/order/adapter/inbound/consumer"
	grpcin "backend-go/internal/modules/order/adapter/inbound/grpc"
	httpin "backend-go/internal/modules/order/adapter/inbound/http"
	jobin "backend-go/internal/modules/order/adapter/inbound/job"
	pgout "backend-go/internal/modules/order/adapter/outbound/postgres"
	redisout "backend-go/internal/modules/order/adapter/outbound/redis"
	"backend-go/internal/modules/order/usecase"
	"backend-go/internal/modules/order/usecase/port"
	"backend-go/internal/pkg/cache"
	"backend-go/internal/pkg/database"
	"backend-go/internal/pkg/httpserver"
	"backend-go/internal/pkg/messaging"
	"backend-go/internal/pkg/reconciliation"

	"github.com/gin-gonic/gin"
	"google.golang.org/grpc"
)

// Deps are the infrastructure handles app/ injects.
type Deps struct {
	DB               *database.DBGroup
	Tx               port.TxManager
	Cache            *cache.Manager // optional
	CacheTTL         time.Duration
	OutboxPartitions int
	Clock            clock.Clock
	IDs              port.IDGenerator
}

// Module wires the order context.
type Module struct {
	http  *httpin.Handler
	grpc  *grpcin.Server
	pay   *consumerin.PaymentEvents
	recon *jobin.OutboxReconciliation
}

// New builds the module's object graph (manual dependency injection).
func New(d Deps) *Module {
	if d.Clock == nil {
		d.Clock = clock.System{}
	}
	if d.IDs == nil {
		d.IDs = ids.Random{}
	}
	var repo port.OrderRepository = pgout.NewOrderRepository(d.DB)
	if d.Cache != nil {
		repo = redisout.NewCachedOrderRepository(repo, d.Cache, d.CacheTTL) // decorator, no use-case change
	}
	outbox := pgout.NewEventOutbox(d.DB, d.OutboxPartitions)

	create := usecase.NewCreateOrder(d.Tx, repo, outbox, d.IDs, d.Clock)
	get := usecase.NewGetOrder(repo)
	paid := usecase.NewMarkOrderPaid(d.Tx, repo, outbox, d.Clock)
	reconcile := usecase.NewReconcileOutbox(d.Tx, repo, outbox, d.Clock)

	return &Module{
		http:  httpin.NewHandler(create, get),
		grpc:  grpcin.NewServer(create, get),
		pay:   consumerin.NewPaymentEvents(paid),
		recon: jobin.NewOutboxReconciliation(reconcile),
	}
}

func (m *Module) Name() string { return "order" }

func (m *Module) RegisterHTTP(r gin.IRouter, mw httpserver.Middlewares) {
	httpin.Register(r, m.http, mw)
}

func (m *Module) RegisterGRPC(s grpc.ServiceRegistrar) { orderv1.RegisterOrderServiceServer(s, m.grpc) }

// Subscriptions lists the streams this module consumes.
func (m *Module) Subscriptions() []messaging.Subscription {
	return []messaging.Subscription{{Stream: "payment", Name: "order/payment-events", Handler: m.pay.Handle}}
}

func (m *Module) Jobs() []reconciliation.Job { return []reconciliation.Job{m.recon} }
