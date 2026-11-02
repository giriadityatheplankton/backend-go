// Package requestid carries the request correlation ID through context.Context
// so HTTP, gRPC, logs, outbox headers and message headers all share one value.
package requestid

import (
	"context"
	"regexp"
)

// Header is the canonical HTTP header and message header name.
const Header = "X-Request-ID"

// MetadataKey is the gRPC metadata key (lower-case by gRPC convention).
const MetadataKey = "x-request-id"

type ctxKey struct{}

var valid = regexp.MustCompile(`^[A-Za-z0-9._\-]{1,128}$`)

// Valid reports whether id is safe to log and echo back (prevents log injection).
func Valid(id string) bool { return valid.MatchString(id) }

// With stores id in ctx.
func With(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, ctxKey{}, id)
}

// From returns the request ID in ctx, or "".
func From(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	id, _ := ctx.Value(ctxKey{}).(string)
	return id
}
