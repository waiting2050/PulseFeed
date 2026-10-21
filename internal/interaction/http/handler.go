package http

import (
	"errors"
	"log/slog"
	"strconv"

	"github.com/gin-gonic/gin"

	"pulsefeed/internal/interaction/app"
	"pulsefeed/internal/interaction/domain"
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
	api.GET("/videos/:videoId/comments", h.ListComments)

	protected := api.Group("")
	protected.Use(auth)
	protected.POST("/videos/:videoId/likes", h.Like)
	protected.DELETE("/videos/:videoId/likes", h.Unlike)
	protected.POST("/videos/:videoId/favorites", h.Favorite)
	protected.DELETE("/videos/:videoId/favorites", h.Unfavorite)
	protected.POST("/videos/:videoId/comments", h.CreateComment)
	protected.DELETE("/comments/:commentId", h.DeleteComment)
}

func (h *Handler) Like(c *gin.Context) {
	h.toggle(c, "like", true)
}

func (h *Handler) Unlike(c *gin.Context) {
	h.toggle(c, "like", false)
}

func (h *Handler) Favorite(c *gin.Context) {
	h.toggle(c, "favorite", true)
}

func (h *Handler) Unfavorite(c *gin.Context) {
	h.toggle(c, "favorite", false)
}

func (h *Handler) toggle(c *gin.Context, action string, active bool) {
	userID, ok := middleware.CurrentUserID(c)
	if !ok {
		httpx.Unauthorized(c, "未登录")
		return
	}

	videoID, ok := parseID(c, "videoId")
	if !ok {
		return
	}

	var err error
	switch {
	case action == "like" && active:
		err = h.service.Like(c.Request.Context(), userID, videoID)
	case action == "like" && !active:
		err = h.service.Unlike(c.Request.Context(), userID, videoID)
	case action == "favorite" && active:
		err = h.service.Favorite(c.Request.Context(), userID, videoID)
	default:
		err = h.service.Unfavorite(c.Request.Context(), userID, videoID)
	}
	if err != nil {
		h.respondError(c, err)
		return
	}

	httpx.OK(c, actionResponse{VideoID: videoID, Action: action, Active: active})
}

func (h *Handler) CreateComment(c *gin.Context) {
	userID, ok := middleware.CurrentUserID(c)
	if !ok {
		httpx.Unauthorized(c, "未登录")
		return
	}

	videoID, ok := parseID(c, "videoId")
	if !ok {
		return
	}

	var req createCommentRequest
	if !httpx.BindJSON(c, &req) {
		return
	}

	comment, err := h.service.CreateComment(c.Request.Context(), userID, videoID, req.Content)
	if err != nil {
		h.respondError(c, err)
		return
	}
	httpx.Created(c, toCommentResponse(comment))
}

func (h *Handler) DeleteComment(c *gin.Context) {
	userID, ok := middleware.CurrentUserID(c)
	if !ok {
		httpx.Unauthorized(c, "未登录")
		return
	}

	commentID, ok := parseID(c, "commentId")
	if !ok {
		return
	}

	if err := h.service.DeleteComment(c.Request.Context(), userID, commentID); err != nil {
		h.respondError(c, err)
		return
	}
	httpx.NoContent(c)
}

func (h *Handler) ListComments(c *gin.Context) {
	videoID, ok := parseID(c, "videoId")
	if !ok {
		return
	}

	limit, _ := strconv.Atoi(c.Query("limit"))
	result, err := h.service.ListComments(c.Request.Context(), videoID, c.Query("cursor"), limit)
	if err != nil {
		h.respondError(c, err)
		return
	}

	items := make([]commentResponse, 0, len(result.Items))
	for _, comment := range result.Items {
		items = append(items, *toCommentResponse(comment))
	}
	httpx.OK(c, commentListResponse{
		Items:      items,
		NextCursor: result.NextCursor,
		HasMore:    result.HasMore,
	})
}

func parseID(c *gin.Context, name string) (int64, bool) {
	id, err := strconv.ParseInt(c.Param(name), 10, 64)
	if err != nil || id <= 0 {
		httpx.BadRequest(c, "ID 不合法")
		return 0, false
	}
	return id, true
}

func (h *Handler) respondError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, domain.ErrVideoNotFound), errors.Is(err, domain.ErrCommentNotFound):
		httpx.NotFound(c, err.Error())
	case errors.Is(err, domain.ErrForbidden):
		httpx.Forbidden(c, err.Error())
	case errors.Is(err, domain.ErrInvalidComment),
		errors.Is(err, domain.ErrInvalidCursor),
		errors.Is(err, domain.ErrInvalidAction):
		httpx.BadRequest(c, err.Error())
	default:
		h.logger.Error("interaction request failed", "error", err)
		httpx.Internal(c, "服务内部错误")
	}
}
