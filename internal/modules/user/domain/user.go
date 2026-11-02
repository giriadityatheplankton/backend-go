//go:build examples

// Package domain holds the user entity.
package domain

import "backend-go/internal/kernel/apperr"

var (
	ErrUserNotFound = apperr.New(apperr.KindNotFound, "USER_NOT_FOUND", "User not found")
	ErrInvalidUser  = apperr.New(apperr.KindInvalid, "INVALID_USER_ID", "User ID must be a positive integer")
)

// User is a read-only example entity (no struct tags: persistence/API shape is an adapter concern).
type User struct {
	ID    int
	Name  string
	Email string
}
