package http

import (
	"errors"
	"log/slog"
	"strconv"

	"github.com/gin-gonic/gin"

	"pulsefeed/internal/relation/app"
	"pulsefeed/internal/relation/domain"
	"pulsefeed/internal/shared/httpx"
	"pulsefeed/internal/shared/middleware"
)

type Handler struct {
	service *app.Service
	logger  *slog.Logger
}

func NewHandler(service *app.Service, logger *slog.Logger) *Handler {
	return &Handler{service: service, logger: logger}
}

func (h *Handler) RegisterRoutes(api gin.IRouter, auth gin.HandlerFunc) {
	protected := api.Group("")
	protected.Use(auth)
	protected.POST("/users/:userId/follow", h.Follow)
	protected.DELETE("/users/:userId/follow", h.Unfollow)
}

func (h *Handler) Follow(c *gin.Context) {
	h.toggle(c, true)
}

func (h *Handler) Unfollow(c *gin.Context) {
	h.toggle(c, false)
}

func (h *Handler) toggle(c *gin.Context, following bool) {
	userID, ok := middleware.CurrentUserID(c)
	if !ok {
		httpx.Unauthorized(c, "未登录")
		return
	}

	targetID, err := strconv.ParseInt(c.Param("userId"), 10, 64)
	if err != nil || targetID <= 0 {
		httpx.BadRequest(c, "用户 ID 不合法")
		return
	}

	if following {
		err = h.service.Follow(c.Request.Context(), userID, targetID)
	} else {
		err = h.service.Unfollow(c.Request.Context(), userID, targetID)
	}
	if err != nil {
		h.respondError(c, err)
		return
	}

	httpx.OK(c, followResponse{UserID: targetID, Following: following})
}

func (h *Handler) respondError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, domain.ErrUserNotFound):
		httpx.NotFound(c, err.Error())
	case errors.Is(err, domain.ErrCannotFollowSelf):
		httpx.BadRequest(c, err.Error())
	default:
		h.logger.Error("relation request failed", "error", err)
		httpx.Internal(c, "服务内部错误")
	}
}
