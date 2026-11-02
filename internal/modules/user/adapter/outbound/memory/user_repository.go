//go:build examples

// Package memory is a stand-in data source for the example (replace with a real database adapter).
package memory

import (
	"context"

	"backend-go/internal/modules/user/domain"
	"backend-go/internal/modules/user/usecase/port"
)

var _ port.UserRepository = (*UserRepository)(nil)

type UserRepository struct{ users map[int]domain.User }

func NewUserRepository() *UserRepository {
	return &UserRepository{users: map[int]domain.User{1: {ID: 1, Name: "Developer", Email: "dev@example.com"}}}
}

func (r *UserRepository) GetByID(ctx context.Context, id int) (*domain.User, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	u, ok := r.users[id]
	if !ok {
		return nil, domain.ErrUserNotFound
	}
	return &u, nil
}
