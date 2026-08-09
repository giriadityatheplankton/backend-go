package repository

import (
	"errors"
	"backend-go/internal/domain"
)

type userRepository struct {
	// In a real application, you would inject a DB connection (e.g., *sql.DB or GORM) here.
}

// NewUserRepository creates a new instance of UserRepository.
func NewUserRepository() domain.UserRepository {
	return &userRepository{}
}

// GetByID returns a User by their ID.
func (r *userRepository) GetByID(id int) (*domain.User, error) {
	// DUMMY IMPLEMENTATION: Simulate a database query.
	if id == 1 {
		return &domain.User{ID: 1, Name: "Developer", Email: "dev@example.com"}, nil
	}
	return nil, errors.New("user not found")
}
