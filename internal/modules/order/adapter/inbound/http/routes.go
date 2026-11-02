//go:build examples

package http

import (
	"backend-go/internal/platform/httpserver"

	"github.com/gin-gonic/gin"
)

// Register mounts the order routes on r (already authenticated by app/).
// Cross-cutting middleware arrives as a parameter; this package builds none.
func Register(r gin.IRouter, h *Handler, mw httpserver.Middlewares) {
	g := r.Group("/orders")
	g.GET("/:id", mw.RequireScope("orders:read"), h.Get)
	g.GET("/:id/strong", mw.RequireScope("orders:read"), h.GetStrong)
	g.POST("", mw.RequireScope("orders:write"), mw.RateLimit, mw.IdempotentRequired, h.Create)
}
