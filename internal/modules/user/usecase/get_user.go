//go:build examples

// Package usecase holds the user application logic.
package usecase

import (
	"context"

	"backend-go/internal/modules/user/domain"
	"backend-go/internal/modules/user/usecase/port"
)

type GetUserOutput struct {
	ID    int
	Name  string
	Email string
}

// GetUser returns one user. It is a pure read: eventual consistency is fine here.
type GetUser struct{ repo port.UserRepository }

func NewGetUser(repo port.UserRepository) *GetUser { return &GetUser{repo: repo} }

func (uc *GetUser) Execute(ctx context.Context, id int) (GetUserOutput, error) {
	if id <= 0 {
		return GetUserOutput{}, domain.ErrInvalidUser
	}
	u, err := uc.repo.GetByID(ctx, id)
	if err != nil {
		return GetUserOutput{}, err
	}
	return GetUserOutput{ID: u.ID, Name: u.Name, Email: u.Email}, nil
}
