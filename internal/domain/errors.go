package domain

import "errors"

var (
	// ErrUserNotFound is returned when the requested user is not found in the system.
	ErrUserNotFound = errors.New("user not found")

	// ErrInvalidUserID is returned when the provided user ID is invalid (e.g. <= 0).
	ErrInvalidUserID = errors.New("invalid user ID")

	// ErrInternal is returned when an unhandled internal error occurs.
	ErrInternal = errors.New("internal server error")
)
