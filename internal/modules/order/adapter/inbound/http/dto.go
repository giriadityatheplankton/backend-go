//go:build examples

package http

import "backend-go/internal/modules/order/usecase"

// createOrderRequest is the transport DTO; JSON tags live here and nowhere in domain.
type createOrderRequest struct {
	CustomerID  string `json:"customer_id" binding:"required,max=128"`
	AmountMinor int64  `json:"amount_minor" binding:"required,gt=0"`
	Currency    string `json:"currency" binding:"required,len=3"`
}

type orderResponse struct {
	ID          string `json:"id"`
	CustomerID  string `json:"customer_id"`
	AmountMinor int64  `json:"amount_minor"`
	Currency    string `json:"currency"`
	Status      string `json:"status"`
	CreatedAt   int64  `json:"created_at_unix"`
	Replayed    bool   `json:"replayed,omitempty"`
}

func toResponse(o usecase.OrderOutput) orderResponse {
	return orderResponse{ID: o.ID, CustomerID: o.CustomerID, AmountMinor: o.AmountMinor, Currency: o.Currency,
		Status: o.Status, CreatedAt: o.CreatedAt, Replayed: o.Replayed}
}
