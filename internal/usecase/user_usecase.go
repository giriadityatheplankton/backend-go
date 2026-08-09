package usecase

import (
	"errors"
	"backend-go/internal/domain"
)

type userUsecase struct {
	repo domain.UserRepository
}

// NewUserUsecase creates a new instance of UserUsecase by injecting UserRepository.
func NewUserUsecase(repo domain.UserRepository) domain.UserUsecase {
	return &userUsecase{
		repo: repo,
	}
}

// GetUser retrieves user data by ID after performing basic business validation.
func (u *userUsecase) GetUser(id int) (*domain.User, error) {
	// Business logic: ID must not be <= 0
	if id <= 0 {
		return nil, errors.New("invalid user ID")
	}

	// Call repository (could be real DB, or mock during unit testing)
	return u.repo.GetByID(id)
}
