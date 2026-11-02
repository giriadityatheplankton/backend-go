package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"backend-go/internal/config"
	"backend-go/internal/platform/auth"
	"backend-go/internal/platform/idempotency"
)

// buildAuthenticator returns nil for AUTH_MODE=none (development only; config
// validation rejects it in production).
func buildAuthenticator(cfg *config.Config) (auth.Authenticator, error) {
	mode, err := auth.ParseMode(cfg.AuthMode)
	if err != nil {
		return nil, err
	}
	switch mode {
	case auth.ModeJWT:
		return auth.NewJWT(auth.JWTConfig{HMACSecret: []byte(cfg.JWTHMACSecret), Issuer: cfg.JWTIssuer, Audience: cfg.JWTAudience})
	case auth.ModeStatic:
		tokens, err := parseStaticTokens(cfg.AuthStaticTokens)
		if err != nil {
			return nil, err
		}
		return auth.NewStaticTokens(tokens), nil
	default:
		return nil, nil
	}
}

// parseStaticTokens parses "token;subject;tenant;scope1,scope2|token2;..." (entries
// separated by "|", fields by ";", scopes by ","; scope names may contain ":").
func parseStaticTokens(raw string) (map[string]auth.Principal, error) {
	out := map[string]auth.Principal{}
	for _, entry := range strings.Split(raw, "|") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		parts := strings.Split(entry, ";")
		if len(parts) != 4 || parts[0] == "" || parts[1] == "" {
			return nil, fmt.Errorf("AUTH_STATIC_TOKENS: entry must be token;subject;tenant;scope1,scope2")
		}
		if len(parts[0]) < 24 {
			return nil, fmt.Errorf("AUTH_STATIC_TOKENS: tokens must be at least 24 characters")
		}
		var scopes []string
		for _, s := range strings.Split(parts[3], ",") {
			if s = strings.TrimSpace(s); s != "" {
				scopes = append(scopes, s)
			}
		}
		out[parts[0]] = auth.Principal{Subject: parts[1], TenantID: parts[2], Scopes: scopes, Kind: "service"}
	}
	if len(out) == 0 {
		return nil, errors.New("AUTH_STATIC_TOKENS: no tokens parsed")
	}
	return out, nil
}

// unavailableStore stands in for Redis when REDIS_REQUIRED=false and Redis is down,
// so the idempotency FailureMode (closed by default) decides the outcome explicitly
// instead of silently skipping dedupe.
type unavailableStore struct{}

var errNoStore = errors.New("idempotency store unavailable: redis is not connected")

func (unavailableStore) Acquire(context.Context, string, string, time.Duration) (idempotency.Lease, *idempotency.Record, error) {
	return idempotency.Lease{}, nil, errNoStore
}
func (unavailableStore) Complete(context.Context, idempotency.Lease, *idempotency.Record, time.Duration) error {
	return errNoStore
}
func (unavailableStore) Release(context.Context, idempotency.Lease) error { return errNoStore }
func (unavailableStore) Extend(context.Context, idempotency.Lease, time.Duration) error {
	return errNoStore
}
