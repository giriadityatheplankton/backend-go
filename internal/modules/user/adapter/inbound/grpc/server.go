//go:build examples

// Package grpc is the gRPC adapter for the user module.
package grpc

import (
	"context"
	"math"

	userv1 "backend-go/gen/proto/user/v1"
	"backend-go/internal/kernel/apperr"
	"backend-go/internal/modules/user/usecase"
	"backend-go/internal/pkg/grpcserver"
)

type userGetter interface {
	Execute(ctx context.Context, id int) (usecase.GetUserOutput, error)
}

type Server struct {
	userv1.UnimplementedUserServiceServer
	get userGetter
}

func NewServer(get userGetter) *Server { return &Server{get: get} }

func (s *Server) GetUser(ctx context.Context, req *userv1.GetUserRequest) (*userv1.GetUserResponse, error) {
	u, err := s.get.Execute(ctx, int(req.GetId()))
	if err != nil {
		return nil, grpcserver.Error(err)
	}
	if u.ID > math.MaxInt32 {
		return nil, grpcserver.Error(apperr.ErrInternal)
	}
	return &userv1.GetUserResponse{Id: int32(u.ID), Name: u.Name, Email: u.Email}, nil //nolint:gosec // u.ID <= MaxInt32 checked above
}
