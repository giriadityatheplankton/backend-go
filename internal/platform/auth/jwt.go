package auth

import (
	"context"
	"crypto/rsa"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// JWTConfig configures bearer-JWT verification.
type JWTConfig struct {
	// HMACSecret verifies HS256 tokens. Use PublicKey for RS256 (recommended across trust boundaries).
	HMACSecret []byte
	PublicKey  *rsa.PublicKey
	Issuer     string
	Audience   string
	Leeway     time.Duration
	// Claim names; defaults: "tenant_id" and "scope" (space separated).
	TenantClaim string
	ScopeClaim  string
}

// JWT authenticates signed JWTs.
type JWT struct{ cfg JWTConfig }

// NewJWT validates cfg and creates the authenticator.
func NewJWT(cfg JWTConfig) (*JWT, error) {
	if len(cfg.HMACSecret) == 0 && cfg.PublicKey == nil {
		return nil, errors.New("jwt: an HMAC secret or an RSA public key is required")
	}
	if len(cfg.HMACSecret) > 0 && len(cfg.HMACSecret) < 32 {
		return nil, errors.New("jwt: HMAC secret must be at least 32 bytes")
	}
	if cfg.TenantClaim == "" {
		cfg.TenantClaim = "tenant_id"
	}
	if cfg.ScopeClaim == "" {
		cfg.ScopeClaim = "scope"
	}
	if cfg.Leeway == 0 {
		cfg.Leeway = 30 * time.Second
	}
	return &JWT{cfg: cfg}, nil
}

func (j *JWT) Authenticate(_ context.Context, token string) (Principal, error) {
	opts := []jwt.ParserOption{
		jwt.WithExpirationRequired(),
		jwt.WithLeeway(j.cfg.Leeway),
		jwt.WithValidMethods(j.allowedMethods()), // blocks alg=none and key-type confusion
	}
	if j.cfg.Issuer != "" {
		opts = append(opts, jwt.WithIssuer(j.cfg.Issuer))
	}
	if j.cfg.Audience != "" {
		opts = append(opts, jwt.WithAudience(j.cfg.Audience))
	}
	claims := jwt.MapClaims{}
	_, err := jwt.NewParser(opts...).ParseWithClaims(token, claims, func(*jwt.Token) (any, error) {
		if j.cfg.PublicKey != nil {
			return j.cfg.PublicKey, nil
		}
		return j.cfg.HMACSecret, nil
	})
	if err != nil {
		return Principal{}, ErrUnauthenticated
	}
	sub, _ := claims.GetSubject()
	if sub == "" {
		return Principal{}, ErrUnauthenticated
	}
	p := Principal{Subject: sub, Kind: "user"}
	if v, ok := claims[j.cfg.TenantClaim].(string); ok {
		p.TenantID = v
	}
	switch v := claims[j.cfg.ScopeClaim].(type) {
	case string:
		p.Scopes = strings.Fields(v)
	case []any:
		for _, s := range v {
			if str, ok := s.(string); ok {
				p.Scopes = append(p.Scopes, str)
			}
		}
	}
	return p, nil
}

func (j *JWT) allowedMethods() []string {
	if j.cfg.PublicKey != nil {
		return []string{"RS256", "RS384", "RS512"}
	}
	return []string{"HS256", "HS384", "HS512"}
}

// SignHS256 issues a token (tests and local tooling).
func SignHS256(secret []byte, claims jwt.MapClaims) (string, error) {
	s, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(secret)
	if err != nil {
		return "", fmt.Errorf("sign jwt: %w", err)
	}
	return s, nil
}
