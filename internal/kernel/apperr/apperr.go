// Package apperr defines the application error taxonomy shared by every layer.
//
// Domain and use-case code return *Error (or wrap one). Delivery adapters map the
// Kind to a transport status in exactly one place: respond.Error for HTTP and
// grpcerr.From for gRPC. This package depends on the standard library only.
package apperr

import (
	"context"
	"errors"
	"fmt"
)

// Kind classifies an error independent of any transport.
type Kind int

const (
	KindInternal           Kind = iota // unexpected failure; details must not leak
	KindInvalid                        // malformed or semantically invalid input
	KindNotFound                       // resource does not exist
	KindConflict                       // state conflict, duplicate, idempotency key reuse
	KindFailedPrecondition             // valid request but system state forbids it
	KindUnauthenticated                // missing or invalid credentials
	KindForbidden                      // authenticated but not allowed
	KindRateLimited                    // caller exceeded a quota
	KindUnavailable                    // dependency down; caller may retry
	KindTimeout                        // deadline exceeded
)

func (k Kind) String() string {
	switch k {
	case KindInvalid:
		return "invalid"
	case KindNotFound:
		return "not_found"
	case KindConflict:
		return "conflict"
	case KindFailedPrecondition:
		return "failed_precondition"
	case KindUnauthenticated:
		return "unauthenticated"
	case KindForbidden:
		return "forbidden"
	case KindRateLimited:
		return "rate_limited"
	case KindUnavailable:
		return "unavailable"
	case KindTimeout:
		return "timeout"
	default:
		return "internal"
	}
}

// Error is a classified application error with a stable machine-readable code.
type Error struct {
	Kind    Kind
	Code    string // stable, UPPER_SNAKE_CASE, safe to expose to clients
	Message string // human readable, safe to expose to clients
	Err     error  // optional cause; never exposed to clients
}

func (e *Error) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("%s: %s: %v", e.Code, e.Message, e.Err)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

func (e *Error) Unwrap() error { return e.Err }

// Is lets errors.Is match two *Error values with the same Code, so package-level
// sentinel errors keep working after being wrapped with a cause.
func (e *Error) Is(target error) bool {
	t, ok := target.(*Error)
	return ok && t.Code != "" && t.Code == e.Code
}

// New creates a classified error.
func New(kind Kind, code, message string) *Error {
	return &Error{Kind: kind, Code: code, Message: message}
}

// Wrap attaches a cause to a classified error without mutating the sentinel.
func Wrap(base *Error, cause error) *Error {
	if base == nil {
		return nil
	}
	cp := *base
	cp.Err = cause
	return &cp
}

// As returns the first *Error in err's chain.
func As(err error) (*Error, bool) {
	var e *Error
	if errors.As(err, &e) {
		return e, true
	}
	return nil, false
}

// KindOf returns the Kind of err; unclassified errors are KindInternal.
func KindOf(err error) Kind {
	if err == nil {
		return KindInternal
	}
	if e, ok := As(err); ok {
		return e.Kind
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return KindTimeout
	}
	return KindInternal
}

// CodeOf returns the stable code of err or INTERNAL_ERROR.
func CodeOf(err error) string {
	if e, ok := As(err); ok {
		return e.Code
	}
	return "INTERNAL_ERROR"
}

// PublicMessage returns a message safe for clients. Internal errors never leak.
func PublicMessage(err error) string {
	if e, ok := As(err); ok && e.Kind != KindInternal {
		return e.Message
	}
	return "An unexpected error occurred"
}

// Common sentinels reused across modules.
var (
	ErrInternal = New(KindInternal, "INTERNAL_ERROR", "An unexpected error occurred")
	ErrInvalid  = New(KindInvalid, "INVALID_REQUEST", "The request is invalid")
)
