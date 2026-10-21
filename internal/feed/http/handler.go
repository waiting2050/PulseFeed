package http

import (
	"errors"
	"log/slog"
	"strconv"

	"github.com/gin-gonic/gin"

	"pulsefeed/internal/feed/app"
	"pulsefeed/internal/feed/domain"
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
	api.GET("/feed-items", h.List)

	protected := api.Group("")
	protected.Use(auth)
	protected.GET("/feed-items/following", h.Following)
}

func (h *Handler) List(c *gin.Context) {
	limit, _ := strconv.Atoi(c.Query("limit"))
	result, err := h.service.List(c.Request.Context(), c.Query("scene"), c.Query("cursor"), limit)
	if err != nil {
		h.respondError(c, err)
		return
	}
	h.respondList(c, result)
}

func (h *Handler) Following(c *gin.Context) {
	userID, ok := middleware.CurrentUserID(c)
	if !ok {
		httpx.Unauthorized(c, "未登录")
		return
	}

	limit, _ := strconv.Atoi(c.Query("limit"))
	result, err := h.service.ListFollowing(c.Request.Context(), userID, c.Query("cursor"), limit)
	if err != nil {
		h.respondError(c, err)
		return
	}
	h.respondList(c, result)
}

func (h *Handler) respondList(c *gin.Context, result *app.ListResult) {
	items := make([]feedItemResponse, 0, len(result.Items))
	for _, item := range result.Items {
		items = append(items, feedItemResponse{
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

	httpx.OK(c, feedListResponse{
		Items:      items,
		NextCursor: result.NextCursor,
		HasMore:    result.HasMore,
	})
}

func (h *Handler) respondError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, domain.ErrLoginRequired):
		httpx.Unauthorized(c, err.Error())
	case errors.Is(err, domain.ErrInvalidScene), errors.Is(err, domain.ErrInvalidCursor):
		httpx.BadRequest(c, err.Error())
	default:
		h.logger.Error("feed request failed", "error", err)
		httpx.Internal(c, "服务内部错误")
	}
}
