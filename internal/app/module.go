package app

import (
	"backend-go/internal/platform/httpserver"
	"backend-go/internal/platform/messaging"
	"backend-go/internal/platform/reconciliation"

	"github.com/gin-gonic/gin"
	"google.golang.org/grpc"
)

// Module is the contract every bounded context implements. app/ knows modules
// only through this interface; modules never import app/ or each other.
type Module interface {
	Name() string
	RegisterHTTP(r gin.IRouter, mw httpserver.Middlewares)
	RegisterGRPC(s grpc.ServiceRegistrar)
	Subscriptions() []messaging.Subscription
	Jobs() []reconciliation.Job
}
