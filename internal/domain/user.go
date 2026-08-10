package domain

import (
	"context"
	"time"
)

// User represents the user data model.
type User struct {
	ID    int    `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email"`
}

// UserAccessedEvent defines the event payload for user access tracking.
type UserAccessedEvent struct {
	UserID     int       `json:"user_id"`
	UserName   string    `json:"user_name"`
	AccessedAt time.Time `json:"accessed_at"`
	Source     string    `json:"source"`
}

// UserRepository defines the data access contract for User.
type UserRepository interface {
	GetByID(ctx context.Context, id int) (*User, error)
}

// UserUsecase defines the business logic contract for User.
type UserUsecase interface {
	GetUser(ctx context.Context, id int) (*User, error)
}

// EventPublisher defines the contract for publishing domain events.
type EventPublisher interface {
	PublishUserAccessed(ctx context.Context, event UserAccessedEvent) error
}

