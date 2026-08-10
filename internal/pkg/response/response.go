package response

import (
	"github.com/gin-gonic/gin"
)

// APIResponse defines the standard API JSON envelope.
type APIResponse struct {
	Success bool      `json:"success"`
	Data    any       `json:"data,omitempty"`
	Error   *APIError `json:"error,omitempty"`
}

// APIError represents structured error details.
type APIError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Success renders a successful JSON response with HTTP status code and payload data.
func Success(c *gin.Context, statusCode int, data any) {
	c.JSON(statusCode, APIResponse{
		Success: true,
		Data:    data,
	})
}

// Error renders an error JSON response with HTTP status code, error code, and error message.
func Error(c *gin.Context, statusCode int, code string, message string) {
	c.JSON(statusCode, APIResponse{
		Success: false,
		Error: &APIError{
			Code:    code,
			Message: message,
		},
	})
}
