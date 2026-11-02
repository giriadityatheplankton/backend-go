package middleware_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"backend-go/internal/platform/httpserver/middleware"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

func corsRouter(cfg middleware.CORSConfig) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(middleware.SecurityHeaders())
	r.Use(middleware.CORS(cfg))
	r.GET("/ping", func(c *gin.Context) { c.String(http.StatusOK, "pong") })
	return r
}

func get(r http.Handler, method, origin string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, "/ping", nil)
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestSecurityHeaders(t *testing.T) {
	w := get(corsRouter(middleware.DefaultCORSConfig()), http.MethodGet, "")
	assert.Equal(t, "nosniff", w.Header().Get("X-Content-Type-Options"))
	assert.Equal(t, "DENY", w.Header().Get("X-Frame-Options"))
	assert.Equal(t, "0", w.Header().Get("X-XSS-Protection"), "the legacy XSS auditor must be disabled")
	assert.NotContains(t, w.Header().Get("Strict-Transport-Security"), "preload")
}

func TestCORSDeniesByDefault(t *testing.T) {
	w := get(corsRouter(middleware.DefaultCORSConfig()), http.MethodGet, "https://evil.example")
	assert.Empty(t, w.Header().Get("Access-Control-Allow-Origin"))
	assert.Empty(t, w.Header().Get("Access-Control-Allow-Credentials"))
}

func TestCORSAllowsOnlyListedOrigins(t *testing.T) {
	cfg := middleware.DefaultCORSConfig()
	cfg.AllowedOrigins, cfg.AllowCredentials = []string{"https://app.example.com"}, true
	r := corsRouter(cfg)

	ok := get(r, http.MethodGet, "https://app.example.com")
	assert.Equal(t, "https://app.example.com", ok.Header().Get("Access-Control-Allow-Origin"))
	assert.Equal(t, "true", ok.Header().Get("Access-Control-Allow-Credentials"))
	assert.Contains(t, ok.Header().Values("Vary"), "Origin")

	bad := get(r, http.MethodGet, "https://evil.example")
	assert.Empty(t, bad.Header().Get("Access-Control-Allow-Origin"))
}

func TestCORSWildcardIsNeverCombinedWithCredentials(t *testing.T) {
	cfg := middleware.DefaultCORSConfig()
	cfg.AllowedOrigins, cfg.AllowCredentials = []string{"*"}, true
	w := get(corsRouter(cfg), http.MethodGet, "https://anything.example")
	assert.Empty(t, w.Header().Get("Access-Control-Allow-Origin"), "* + credentials would let any site act as the user")

	cfg.AllowCredentials = false
	w = get(corsRouter(cfg), http.MethodGet, "https://anything.example")
	assert.Equal(t, "*", w.Header().Get("Access-Control-Allow-Origin"))
}

func TestCORSPreflight(t *testing.T) {
	cfg := middleware.DefaultCORSConfig()
	cfg.AllowedOrigins = []string{"https://app.example.com"}
	w := get(corsRouter(cfg), http.MethodOptions, "https://app.example.com")
	assert.Equal(t, http.StatusNoContent, w.Code)
	assert.Contains(t, w.Header().Get("Access-Control-Allow-Headers"), "Idempotency-Key")
}
