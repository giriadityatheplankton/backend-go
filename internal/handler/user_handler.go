package handler

import (
	"errors"
	"net/http"
	"strconv"

	"backend-go/internal/domain"
	"backend-go/internal/pkg/response"

	"github.com/gin-gonic/gin"
)

// UserHandler holds a reference to the logic layer (usecase).
type UserHandler struct {
	usecase domain.UserUsecase
}

// RegisterUserRoutes registers routes related to users.
func RegisterUserRoutes(r *gin.Engine, us domain.UserUsecase) {
	handler := &UserHandler{usecase: us}
	r.GET("/users/:id", handler.GetByID)
}

// GetByID handles HTTP requests to retrieve user data by ID.
func (h *UserHandler) GetByID(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.Atoi(idStr)
	if err != nil {
		response.Error(c, http.StatusBadRequest, "INVALID_ID_FORMAT", "ID must be a valid integer")
		return
	}

	user, err := h.usecase.GetUser(c.Request.Context(), id)
	if err != nil {
		switch {
		case errors.Is(err, domain.ErrInvalidUserID):
			response.Error(c, http.StatusBadRequest, "INVALID_USER_ID", err.Error())
		case errors.Is(err, domain.ErrUserNotFound):
			response.Error(c, http.StatusNotFound, "USER_NOT_FOUND", err.Error())
		default:
			response.Error(c, http.StatusInternalServerError, "INTERNAL_SERVER_ERROR", "An unexpected error occurred")
		}
		return
	}

	response.Success(c, http.StatusOK, user)
}
