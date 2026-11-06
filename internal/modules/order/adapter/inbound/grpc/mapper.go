//go:build examples

package grpc

import (
	orderv1 "backend-go/gen/proto/order/v1"
	"backend-go/internal/modules/order/usecase"
)

func toProto(o usecase.OrderOutput) *orderv1.Order {
	return &orderv1.Order{
		Id: o.ID, CustomerId: o.CustomerID, AmountMinor: o.AmountMinor, Currency: o.Currency,
		Status: o.Status, CreatedAtUnix: o.CreatedAt, Replayed: o.Replayed,
	}
}
