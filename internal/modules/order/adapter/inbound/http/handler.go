//go:build examples

// Package http is the REST adapter for the order module: parse, call the use
// case, present. No business logic.
package http

import (
	"context"
	nethttp "net/http"
	"strings"

	"backend-go/internal/modules/order/usecase"
	"backend-go/internal/modules/order/usecase/port"
	"backend-go/internal/pkg/httpserver/respond"
	"backend-go/internal/pkg/idempotency"

	"github.com/gin-gonic/gin"
)

// Small interfaces owned by the controller (accept interfaces, return structs).
type (
	orderCreator interface {
		Execute(ctx context.Context, in usecase.CreateOrderInput) (usecase.OrderOutput, error)
	}
	orderReader interface {
		Execute(ctx context.Context, in usecase.GetOrderInput) (usecase.OrderOutput, error)
	}
)

type Handler struct {
	create orderCreator
	get    orderReader
}

func NewHandler(create orderCreator, get orderReader) *Handler {
	return &Handler{create: create, get: get}
}

func (h *Handler) Create(c *gin.Context) {
	var req createOrderRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respond.Invalid(c, err)
		return
	}
	out, err := h.create.Execute(c.Request.Context(), usecase.CreateOrderInput{
		IdempotencyKey: idempotencyKey(c),
		CustomerID:     req.CustomerID,
		AmountMinor:    req.AmountMinor,
		Currency:       req.Currency,
	})
	if err != nil {
		respond.Fail(c, err)
		return
	}
	status := nethttp.StatusCreated
	if out.Replayed {
		status = nethttp.StatusOK
	} else {
		c.Header("Location", "/v1/orders/"+out.ID)
	}
	respond.Success(c, status, toResponse(out))
}

func (h *Handler) Get(c *gin.Context)       { h.read(c, port.EventuallyConsistent) }
func (h *Handler) GetStrong(c *gin.Context) { h.read(c, port.Strong) }

func (h *Handler) read(c *gin.Context, consistency port.Consistency) {
	out, err := h.get.Execute(c.Request.Context(), usecase.GetOrderInput{ID: c.Param("id"), Consistency: consistency})
	if err != nil {
		respond.Fail(c, err)
		return
	}
	respond.Success(c, nethttp.StatusOK, toResponse(out))
}

func idempotencyKey(c *gin.Context) string {
	if k := idempotency.KeyFrom(c.Request.Context()); k != "" {
		return k
	}
	if k := strings.TrimSpace(c.GetHeader(idempotency.HeaderIdempotencyKey)); k != "" {
		return k
	}
	return strings.TrimSpace(c.GetHeader(idempotency.HeaderLegacyIdempotencyKey))
}
