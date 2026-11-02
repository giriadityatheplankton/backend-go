package middleware

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
)

// SecurityHeaders attaches OWASP-recommended security headers. HSTS preload is
// intentionally not set; opt in at the edge once every sub-domain is HTTPS-only.
func SecurityHeaders() gin.HandlerFunc {
	return func(c *gin.Context) {
		h := c.Writer.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("X-XSS-Protection", "0") // legacy auditor is harmful; modern browsers ignore it
		h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
		h.Set("Permissions-Policy", "geolocation=(), microphone=(), camera=()")
		c.Next()
	}
}

// CORSConfig defines the cross-origin policy. The zero value denies all
// cross-origin requests (no CORS headers are emitted).
type CORSConfig struct {
	AllowedOrigins   []string // exact origins; "*" is honoured only when AllowCredentials is false
	AllowedMethods   []string
	AllowedHeaders   []string
	AllowCredentials bool
	MaxAgeSeconds    int
}

// DefaultCORSConfig is deny-by-default. Populate AllowedOrigins from
// configuration (CORS_ALLOWED_ORIGINS). Service-to-service APIs need no CORS.
func DefaultCORSConfig() CORSConfig {
	return CORSConfig{
		AllowedMethods: []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
		AllowedHeaders: []string{"Origin", "Content-Type", "Accept", "Authorization", "X-Request-ID", "Idempotency-Key", "X-Idempotency-Key", "traceparent"},
		MaxAgeSeconds:  600,
	}
}

// CORS provides cross-origin resource sharing middleware.
func CORS(cfg CORSConfig) gin.HandlerFunc {
	if len(cfg.AllowedMethods) == 0 {
		cfg.AllowedMethods = DefaultCORSConfig().AllowedMethods
	}
	if len(cfg.AllowedHeaders) == 0 {
		cfg.AllowedHeaders = DefaultCORSConfig().AllowedHeaders
	}
	methods := strings.Join(cfg.AllowedMethods, ", ")
	headers := strings.Join(cfg.AllowedHeaders, ", ")
	allowed := make(map[string]struct{}, len(cfg.AllowedOrigins))
	wildcard := false
	for _, o := range cfg.AllowedOrigins {
		if o == "*" {
			wildcard = !cfg.AllowCredentials // "*" with credentials would allow any site to act as the user
			continue
		}
		allowed[o] = struct{}{}
	}

	return func(c *gin.Context) {
		origin := c.GetHeader("Origin")
		if origin != "" {
			c.Writer.Header().Add("Vary", "Origin")
			_, ok := allowed[origin]
			if ok || wildcard {
				if wildcard && !ok {
					c.Header("Access-Control-Allow-Origin", "*")
				} else {
					c.Header("Access-Control-Allow-Origin", origin)
				}
				c.Header("Access-Control-Allow-Methods", methods)
				c.Header("Access-Control-Allow-Headers", headers)
				if cfg.AllowCredentials {
					c.Header("Access-Control-Allow-Credentials", "true")
				}
				if cfg.MaxAgeSeconds > 0 {
					c.Header("Access-Control-Max-Age", strconv.Itoa(cfg.MaxAgeSeconds))
				}
			}
		}
		if c.Request.Method == http.MethodOptions && origin != "" {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	}
}
