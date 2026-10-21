package http

import (
	"errors"
	"log/slog"
	"strconv"

	"github.com/gin-gonic/gin"

	"pulsefeed/internal/message/app"
	"pulsefeed/internal/message/domain"
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
	protected.GET("/notifications", h.List)
	protected.GET("/notifications/unread-count", h.UnreadCount)
	protected.POST("/notifications/read", h.MarkAllRead)
}

func (h *Handler) List(c *gin.Context) {
	userID, ok := middleware.CurrentUserID(c)
	if !ok {
		httpx.Unauthorized(c, "未登录")
		return
	}

	limit, _ := strconv.Atoi(c.Query("limit"))
	result, err := h.service.List(c.Request.Context(), userID, c.Query("cursor"), limit)
	if err != nil {
		h.respondError(c, err)
		return
	}

	items := make([]notificationResponse, 0, len(result.Items))
	for _, notification := range result.Items {
		items = append(items, notificationResponse{
			ID:            notification.ID,
			Type:          notification.Type,
			ActorID:       notification.ActorID,
			ActorNickname: notification.ActorNickname,
			ActorAvatar:   notification.ActorAvatar,
			VideoID:       notification.VideoID,
			CommentID:     notification.CommentID,
			Content:       notification.Content,
			IsRead:        notification.IsRead,
			CreatedAt:     notification.CreatedAt,
		})
	}

	httpx.OK(c, notificationListResponse{
		Items:      items,
		NextCursor: result.NextCursor,
		HasMore:    result.HasMore,
	})
}

func (h *Handler) UnreadCount(c *gin.Context) {
	userID, ok := middleware.CurrentUserID(c)
	if !ok {
		httpx.Unauthorized(c, "未登录")
		return
	}

	count, err := h.service.UnreadCount(c.Request.Context(), userID)
	if err != nil {
		h.respondError(c, err)
		return
	}
	httpx.OK(c, unreadCountResponse{Count: count})
}

func (h *Handler) MarkAllRead(c *gin.Context) {
	userID, ok := middleware.CurrentUserID(c)
	if !ok {
		httpx.Unauthorized(c, "未登录")
		return
	}

	if err := h.service.MarkAllRead(c.Request.Context(), userID); err != nil {
		h.respondError(c, err)
		return
	}
	httpx.NoContent(c)
}

func (h *Handler) respondError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, domain.ErrInvalidCursor):
		httpx.BadRequest(c, err.Error())
	default:
		h.logger.Error("notification request failed", "error", err)
		httpx.Internal(c, "服务内部错误")
	}
}
