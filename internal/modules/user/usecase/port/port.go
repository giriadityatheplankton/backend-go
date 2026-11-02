//go:build examples

// Package port declares what the user use cases need.
package port

import (
	"context"

	"backend-go/internal/modules/user/domain"
)

// UserRepository reads users. Implementations may cache; GetByID returns domain.ErrUserNotFound.
type UserRepository interface {
	GetByID(ctx context.Context, id int) (*domain.User, error)
}
