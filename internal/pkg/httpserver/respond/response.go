// Package respond renders the standard JSON envelope and maps application
// errors (kernel/apperr) to HTTP statuses in one place.
package respond

import (
	"errors"
	"log/slog"
	"net/http"

	"backend-go/internal/kernel/apperr"
	"backend-go/internal/pkg/observability/requestid"

	"github.com/gin-gonic/gin"
	"github.com/gin-gonic/gin/binding"
	"github.com/go-playground/validator/v10"
)

// APIResponse defines the standard API JSON envelope.
type APIResponse struct {
	Success bool      `json:"success"`
	Data    any       `json:"data,omitempty"`
	Error   *APIError `json:"error,omitempty"`
}

// APIError represents structured error details.
type APIError struct {
	Code      string        `json:"code"`
	Message   string        `json:"message"`
	RequestID string        `json:"request_id,omitempty"`
	Details   []FieldDetail `json:"details,omitempty"`
}

// FieldDetail describes a single validation problem.
type FieldDetail struct {
	Field  string `json:"field"`
	Reason string `json:"reason"`
}

// Success renders a successful JSON response.
func Success(c *gin.Context, statusCode int, data any) {
	c.JSON(statusCode, APIResponse{Success: true, Data: data})
}

// Error renders an error JSON response with an explicit status, code and message.
func Error(c *gin.Context, statusCode int, code string, message string) {
	render(c, statusCode, &APIError{Code: code, Message: message})
}

// Fail maps err to an HTTP response. Unclassified errors become a generic 500
// and are logged; internal details never reach the client.
func Fail(c *gin.Context, err error) {
	kind := apperr.KindOf(err)
	if kind == apperr.KindInternal {
		slog.ErrorContext(c.Request.Context(), "Unhandled error in HTTP handler", "error", err)
	}
	render(c, StatusOf(kind), &APIError{Code: apperr.CodeOf(err), Message: apperr.PublicMessage(err)})
}

// Invalid renders a 400 for request binding/validation failures with per-field details.
func Invalid(c *gin.Context, err error) {
	apiErr := &APIError{Code: "INVALID_REQUEST", Message: "The request body is invalid"}
	var verrs validator.ValidationErrors
	if errors.As(err, &verrs) {
		for _, fe := range verrs {
			apiErr.Details = append(apiErr.Details, FieldDetail{Field: fe.Field(), Reason: fe.Tag()})
		}
	}
	render(c, http.StatusBadRequest, apiErr)
}

// StatusOf maps an error Kind to an HTTP status code.
func StatusOf(k apperr.Kind) int {
	switch k {
	case apperr.KindInvalid:
		return http.StatusBadRequest
	case apperr.KindNotFound:
		return http.StatusNotFound
	case apperr.KindConflict:
		return http.StatusConflict
	case apperr.KindFailedPrecondition:
		return http.StatusUnprocessableEntity
	case apperr.KindUnauthenticated:
		return http.StatusUnauthorized
	case apperr.KindForbidden:
		return http.StatusForbidden
	case apperr.KindRateLimited:
		return http.StatusTooManyRequests
	case apperr.KindUnavailable:
		return http.StatusServiceUnavailable
	case apperr.KindTimeout:
		return http.StatusGatewayTimeout
	default:
		return http.StatusInternalServerError
	}
}

// StrictJSON makes ShouldBindJSON reject unknown fields. Call once at startup.
func StrictJSON() { binding.EnableDecoderDisallowUnknownFields = true }

func render(c *gin.Context, statusCode int, e *APIError) {
	e.RequestID = requestid.From(c.Request.Context())
	c.JSON(statusCode, APIResponse{Success: false, Error: e})
}
