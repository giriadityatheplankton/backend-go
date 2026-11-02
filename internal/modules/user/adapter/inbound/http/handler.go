//go:build examples

// Package http is the REST adapter for the user module.
package http

import (
	"context"
	nethttp "net/http"
	"strconv"

	"backend-go/internal/modules/user/domain"
	"backend-go/internal/modules/user/usecase"
	"backend-go/internal/platform/httpserver"
	"backend-go/internal/platform/httpserver/respond"

	"github.com/gin-gonic/gin"
)

type userGetter interface {
	Execute(ctx context.Context, id int) (usecase.GetUserOutput, error)
}

type Handler struct{ get userGetter }

func NewHandler(get userGetter) *Handler { return &Handler{get: get} }

type userResponse struct {
	ID    int    `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email"`
}

func Register(r gin.IRouter, h *Handler, mw httpserver.Middlewares) {
	r.GET("/users/:id", mw.RequireScope("users:read"), h.GetByID)
}

func (h *Handler) GetByID(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		respond.Fail(c, domain.ErrInvalidUser)
		return
	}
	u, err := h.get.Execute(c.Request.Context(), id)
	if err != nil {
		respond.Fail(c, err)
		return
	}
	respond.Success(c, nethttp.StatusOK, userResponse{ID: u.ID, Name: u.Name, Email: u.Email})
}
