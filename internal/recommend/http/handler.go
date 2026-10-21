package http

import (
	"errors"
	"log/slog"
	"strconv"

	"github.com/gin-gonic/gin"

	feeddomain "pulsefeed/internal/feed/domain"
	"pulsefeed/internal/recommend/app"
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
	protected.GET("/feed-items/recommend", h.List)
}

func (h *Handler) List(c *gin.Context) {
	userID, ok := middleware.CurrentUserID(c)
	if !ok {
		httpx.Unauthorized(c, "未登录")
		return
	}

	limit, _ := strconv.Atoi(c.Query("limit"))
	items, err := h.service.List(c.Request.Context(), userID, limit)
	if err != nil {
		if errors.Is(err, feeddomain.ErrLoginRequired) {
			httpx.Unauthorized(c, err.Error())
			return
		}
		h.logger.Error("recommend request failed", "error", err)
		httpx.Internal(c, "服务内部错误")
		return
	}

	responses := make([]recommendItemResponse, 0, len(items))
	for _, item := range items {
		responses = append(responses, recommendItemResponse{
			VideoID:       item.VideoID,
			AuthorID:      item.AuthorID,
			Title:         item.Title,
			MediaURL:      item.MediaURL,
			CoverURL:      item.CoverURL,
			LikeCount:     item.LikeCount,
			CommentCount:  item.CommentCount,
			FavoriteCount: item.FavoriteCount,
			PublishedAt:   item.PublishedAt,
		})
	}
	httpx.OK(c, recommendResponse{Items: responses})
}
