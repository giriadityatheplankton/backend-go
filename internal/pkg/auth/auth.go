// Package auth establishes WHO is calling and puts a Principal into the context.
// Rate limiting, idempotency scoping, audit and tenant routing must read the
// principal from here, never from client-controlled headers such as X-Tenant-ID.
package auth

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"slices"
	"strings"

	"backend-go/internal/kernel/apperr"
)

// Principal is the authenticated caller.
type Principal struct {
	Subject  string // user or service identity
	TenantID string
	Scopes   []string
	// Kind is "user" or "service".
	Kind string
}

// HasScope reports whether the principal holds scope (or the "*" wildcard).
func (p Principal) HasScope(scope string) bool {
	return slices.Contains(p.Scopes, scope) || slices.Contains(p.Scopes, "*")
}

// Key returns a stable, non-spoofable identifier for rate limiting and key scoping.
func (p Principal) Key() string {
	if p.TenantID != "" {
		return "tenant:" + p.TenantID + ":sub:" + p.Subject
	}
	return "sub:" + p.Subject
}

type ctxKey struct{}

// With stores p in ctx.
func With(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, ctxKey{}, p)
}

// From returns the principal in ctx.
func From(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(ctxKey{}).(Principal)
	return p, ok
}

// ScopeKey returns the idempotency/rate-limit namespace for ctx: the principal
// key, or "anonymous" for unauthenticated calls.
func ScopeKey(ctx context.Context) string {
	if p, ok := From(ctx); ok {
		return p.Key()
	}
	return "anonymous"
}

// Domain errors.
var (
	ErrUnauthenticated = apperr.New(apperr.KindUnauthenticated, "UNAUTHENTICATED", "Authentication is required")
	ErrForbidden       = apperr.New(apperr.KindForbidden, "FORBIDDEN", "You do not have permission to perform this action")
)

// Authenticator validates a bearer credential.
type Authenticator interface {
	Authenticate(ctx context.Context, token string) (Principal, error)
}

// StaticTokens authenticates service-to-service calls with pre-shared tokens.
// Tokens are compared in constant time. Prefer mTLS or JWT where possible.
type StaticTokens struct {
	tokens map[string]Principal
}

// NewStaticTokens builds an authenticator from token → principal.
func NewStaticTokens(tokens map[string]Principal) *StaticTokens { return &StaticTokens{tokens: tokens} }

func (s *StaticTokens) Authenticate(_ context.Context, token string) (Principal, error) {
	var match Principal
	found := 0
	for t, p := range s.tokens {
		if subtle.ConstantTimeCompare([]byte(t), []byte(token)) == 1 {
			match, found = p, 1
		}
	}
	if found == 0 {
		return Principal{}, ErrUnauthenticated
	}
	return match, nil
}

// BearerToken extracts the token from an "Authorization: Bearer <token>" value.
func BearerToken(header string) (string, error) {
	scheme, token, ok := strings.Cut(strings.TrimSpace(header), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") || strings.TrimSpace(token) == "" {
		return "", errors.New("authorization header must be 'Bearer <token>'")
	}
	return strings.TrimSpace(token), nil
}

// Mode selects the authentication strategy.
type Mode string

const (
	ModeNone   Mode = "none" // development only; Config.Validate rejects it in production
	ModeStatic Mode = "static"
	ModeJWT    Mode = "jwt"
)

// ParseMode validates a mode string.
func ParseMode(s string) (Mode, error) {
	switch m := Mode(strings.ToLower(s)); m {
	case "", ModeNone:
		return ModeNone, nil
	case ModeStatic, ModeJWT:
		return m, nil
	}
	return "", fmt.Errorf("unsupported AUTH_MODE %q (use none, static or jwt)", s)
}
