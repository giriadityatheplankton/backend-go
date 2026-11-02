//go:build examples

// Package user is the user bounded context.
package user

import (
	"time"

	userv1 "backend-go/gen/proto/user/v1"
	grpcin "backend-go/internal/modules/user/adapter/inbound/grpc"
	httpin "backend-go/internal/modules/user/adapter/inbound/http"
	memout "backend-go/internal/modules/user/adapter/outbound/memory"
	redisout "backend-go/internal/modules/user/adapter/outbound/redis"
	"backend-go/internal/modules/user/usecase"
	"backend-go/internal/modules/user/usecase/port"
	"backend-go/internal/pkg/cache"
	"backend-go/internal/pkg/httpserver"
	"backend-go/internal/pkg/messaging"
	"backend-go/internal/pkg/reconciliation"

	"github.com/gin-gonic/gin"
	"google.golang.org/grpc"
)

type Deps struct {
	Cache    *cache.Manager // optional
	CacheTTL time.Duration
}

type Module struct {
	http *httpin.Handler
	grpc *grpcin.Server
}

func New(d Deps) *Module {
	var repo port.UserRepository = memout.NewUserRepository()
	if d.Cache != nil {
		repo = redisout.NewCachedUserRepository(repo, d.Cache, d.CacheTTL)
	}
	get := usecase.NewGetUser(repo)
	return &Module{http: httpin.NewHandler(get), grpc: grpcin.NewServer(get)}
}

func (m *Module) Name() string { return "user" }

func (m *Module) RegisterHTTP(r gin.IRouter, mw httpserver.Middlewares) {
	httpin.Register(r, m.http, mw)
}

func (m *Module) RegisterGRPC(s grpc.ServiceRegistrar) { userv1.RegisterUserServiceServer(s, m.grpc) }

func (m *Module) Subscriptions() []messaging.Subscription { return nil }

func (m *Module) Jobs() []reconciliation.Job { return nil }
