package handler

import (
	"net/http"
	"strconv"

	"backend-go/internal/domain"

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

// GetByID handles HTTP requests to retrieve user data by the ID parameter.
func (h *UserHandler) GetByID(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.Atoi(idStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid ID format"})
		return
	}

	user, err := h.usecase.GetUser(id)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, user)
}
