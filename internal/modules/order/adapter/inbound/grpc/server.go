//go:build examples

// Package grpc is the gRPC adapter for the order module.
package grpc

import (
	"context"

	orderv1 "backend-go/gen/proto/order/v1"
	"backend-go/internal/modules/order/usecase"
	"backend-go/internal/modules/order/usecase/port"
	"backend-go/internal/pkg/grpcserver"
	"backend-go/internal/pkg/idempotency"
)

type (
	orderCreator interface {
		Execute(ctx context.Context, in usecase.CreateOrderInput) (usecase.OrderOutput, error)
	}
	orderReader interface {
		Execute(ctx context.Context, in usecase.GetOrderInput) (usecase.OrderOutput, error)
	}
)

// Server implements orderv1.OrderServiceServer by calling the same use cases as HTTP.
type Server struct {
	orderv1.UnimplementedOrderServiceServer
	create orderCreator
	get    orderReader
}

func NewServer(create orderCreator, get orderReader) *Server {
	return &Server{create: create, get: get}
}

func (s *Server) CreateOrder(ctx context.Context, req *orderv1.CreateOrderRequest) (*orderv1.Order, error) {
	out, err := s.create.Execute(ctx, usecase.CreateOrderInput{
		IdempotencyKey: idempotency.GRPCKey(ctx),
		CustomerID:     req.GetCustomerId(),
		AmountMinor:    req.GetAmountMinor(),
		Currency:       req.GetCurrency(),
	})
	if err != nil {
		return nil, grpcserver.Error(err)
	}
	return toProto(out), nil
}

func (s *Server) GetOrder(ctx context.Context, req *orderv1.GetOrderRequest) (*orderv1.Order, error) {
	return s.read(ctx, req.GetId(), port.EventuallyConsistent)
}

func (s *Server) GetOrderStrong(ctx context.Context, req *orderv1.GetOrderRequest) (*orderv1.Order, error) {
	return s.read(ctx, req.GetId(), port.Strong)
}

func (s *Server) read(ctx context.Context, id string, c port.Consistency) (*orderv1.Order, error) {
	out, err := s.get.Execute(ctx, usecase.GetOrderInput{ID: id, Consistency: c})
	if err != nil {
		return nil, grpcserver.Error(err)
	}
	return toProto(out), nil
}
