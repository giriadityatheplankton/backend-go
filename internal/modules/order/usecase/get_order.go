//go:build examples

package usecase

import (
	"context"
	"strings"

	"backend-go/internal/modules/order/domain"
	"backend-go/internal/modules/order/usecase/port"
)

// GetOrderInput selects the order and the freshness the caller needs.
type GetOrderInput struct {
	ID          string
	Consistency port.Consistency
}

// GetOrder reads one order. Handlers pick Strong for read-after-write and for
// decisions about money or state; display reads may be eventually consistent.
type GetOrder struct{ repo port.OrderRepository }

func NewGetOrder(repo port.OrderRepository) *GetOrder { return &GetOrder{repo: repo} }

func (uc *GetOrder) Execute(ctx context.Context, in GetOrderInput) (OrderOutput, error) {
	id := strings.TrimSpace(in.ID)
	if id == "" || len(id) > 64 {
		return OrderOutput{}, domain.ErrInvalidOrder
	}
	o, err := uc.repo.GetByID(ctx, id, in.Consistency)
	if err != nil {
		return OrderOutput{}, err
	}
	return toOutput(o, false), nil
}
