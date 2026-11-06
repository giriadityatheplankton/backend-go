package auth_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"backend-go/internal/pkg/auth"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var secret = []byte(strings.Repeat("s", 32))

func token(t *testing.T, claims jwt.MapClaims) string {
	s, err := auth.SignHS256(secret, claims)
	require.NoError(t, err)
	return s
}

func TestJWTAuthenticate(t *testing.T) {
	a, err := auth.NewJWT(auth.JWTConfig{HMACSecret: secret, Issuer: "iss", Audience: "api"})
	require.NoError(t, err)
	exp := time.Now().Add(time.Hour).Unix()

	p, err := a.Authenticate(context.Background(), token(t, jwt.MapClaims{
		"sub": "u1", "tenant_id": "t1", "scope": "orders:write orders:read", "iss": "iss", "aud": "api", "exp": exp}))
	require.NoError(t, err)
	assert.Equal(t, "u1", p.Subject)
	assert.Equal(t, "t1", p.TenantID)
	assert.True(t, p.HasScope("orders:write"))
	assert.False(t, p.HasScope("admin"))

	for name, claims := range map[string]jwt.MapClaims{
		"expired":        {"sub": "u1", "iss": "iss", "aud": "api", "exp": time.Now().Add(-time.Hour).Unix()},
		"wrong issuer":   {"sub": "u1", "iss": "evil", "aud": "api", "exp": exp},
		"wrong audience": {"sub": "u1", "iss": "iss", "aud": "other", "exp": exp},
		"no subject":     {"iss": "iss", "aud": "api", "exp": exp},
		"no expiry":      {"sub": "u1", "iss": "iss", "aud": "api"},
	} {
		_, err := a.Authenticate(context.Background(), token(t, claims))
		assert.ErrorIs(t, err, auth.ErrUnauthenticated, name)
	}

	none, _ := jwt.NewWithClaims(jwt.SigningMethodNone, jwt.MapClaims{"sub": "u1", "exp": exp}).SignedString(jwt.UnsafeAllowNoneSignatureType)
	_, err = a.Authenticate(context.Background(), none)
	assert.ErrorIs(t, err, auth.ErrUnauthenticated, "alg=none must be rejected")
}

func TestNewJWTRejectsWeakSecret(t *testing.T) {
	_, err := auth.NewJWT(auth.JWTConfig{HMACSecret: []byte("short")})
	assert.Error(t, err)
}

func TestHTTPMiddlewareAndScopes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	a := auth.NewStaticTokens(map[string]auth.Principal{
		"tok-rw": {Subject: "svc", TenantID: "t1", Scopes: []string{"orders:write"}},
		"tok-ro": {Subject: "svc2", Scopes: []string{"orders:read"}},
	})
	r := gin.New()
	r.Use(auth.HTTPMiddleware(a, "/healthz/live"))
	r.GET("/healthz/live", func(c *gin.Context) { c.Status(200) })
	r.POST("/orders", auth.RequireScope(a, "orders:write"), func(c *gin.Context) {
		p, _ := auth.From(c.Request.Context())
		c.String(200, p.Key())
	})
	do := func(path, authz string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, path, nil)
		if authz != "" {
			req.Header.Set("Authorization", authz)
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}
	assert.Equal(t, 401, do("/orders", "").Code)
	assert.Equal(t, 401, do("/orders", "Bearer nope").Code)
	assert.Equal(t, 403, do("/orders", "Bearer tok-ro").Code)
	w := do("/orders", "Bearer tok-rw")
	assert.Equal(t, 200, w.Code)
	assert.Equal(t, "tenant:t1:sub:svc", w.Body.String())

	req := httptest.NewRequest(http.MethodGet, "/healthz/live", nil)
	w2 := httptest.NewRecorder()
	r.ServeHTTP(w2, req)
	assert.Equal(t, 200, w2.Code, "exempt path needs no credentials")
}

func TestScopeKeyIgnoresClientHeaders(t *testing.T) {
	assert.Equal(t, "anonymous", auth.ScopeKey(context.Background()))
	ctx := auth.With(context.Background(), auth.Principal{Subject: "u", TenantID: "t"})
	assert.Equal(t, "tenant:t:sub:u", auth.ScopeKey(ctx))
}
