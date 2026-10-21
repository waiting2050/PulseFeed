package http

import (
	"errors"
	"log/slog"
	"strconv"

	"github.com/gin-gonic/gin"

	"pulsefeed/internal/shared/httpx"
	"pulsefeed/internal/shared/middleware"
	"pulsefeed/internal/video/app"
	"pulsefeed/internal/video/domain"
)

type Handler struct {
	service *app.Service
	logger  *slog.Logger
}

func NewHandler(service *app.Service, logger *slog.Logger) *Handler {
	return &Handler{service: service, logger: logger}
}

func (h *Handler) RegisterRoutes(api gin.IRouter, auth gin.HandlerFunc) {
	api.GET("/videos/:videoId", h.Get)
	api.GET("/profiles/:userId/videos", h.AuthorVideos)

	protected := api.Group("")
	protected.Use(auth)
	protected.POST("/videos", h.Create)
	protected.GET("/users/me/videos", h.MyVideos)
}

func (h *Handler) Create(c *gin.Context) {
	userID, ok := middleware.CurrentUserID(c)
	if !ok {
		httpx.Unauthorized(c, "未登录")
		return
	}

	var req createVideoRequest
	if !httpx.BindJSON(c, &req) {
		return
	}

	video, err := h.service.Create(c.Request.Context(), app.CreateInput{
		AuthorID:       userID,
		Title:          req.Title,
		MediaURL:       req.MediaURL,
		CoverURL:       req.CoverURL,
		IdempotencyKey: c.GetHeader("Idempotency-Key"),
	})
	if err != nil {
		h.respondError(c, err)
		return
	}
	httpx.Created(c, toVideoResponse(video))
}

func (h *Handler) Get(c *gin.Context) {
	videoID, err := strconv.ParseInt(c.Param("videoId"), 10, 64)
	if err != nil || videoID <= 0 {
		httpx.BadRequest(c, "视频 ID 不合法")
		return
	}

	video, err := h.service.Get(c.Request.Context(), videoID)
	if err != nil {
		h.respondError(c, err)
		return
	}
	httpx.OK(c, toVideoResponse(video))
}

func (h *Handler) MyVideos(c *gin.Context) {
	userID, ok := middleware.CurrentUserID(c)
	if !ok {
		httpx.Unauthorized(c, "未登录")
		return
	}
	h.list(c, userID)
}

func (h *Handler) AuthorVideos(c *gin.Context) {
	userID, err := strconv.ParseInt(c.Param("userId"), 10, 64)
	if err != nil || userID <= 0 {
		httpx.BadRequest(c, "用户 ID 不合法")
		return
	}
	h.list(c, userID)
}

func (h *Handler) list(c *gin.Context, authorID int64) {
	limit, _ := strconv.Atoi(c.Query("limit"))
	result, err := h.service.ListByAuthor(c.Request.Context(), authorID, c.Query("cursor"), limit)
	if err != nil {
		h.respondError(c, err)
		return
	}

	items := make([]videoResponse, 0, len(result.Items))
	for _, video := range result.Items {
		items = append(items, *toVideoResponse(video))
	}
	httpx.OK(c, videoListResponse{
		Items:      items,
		NextCursor: result.NextCursor,
		HasMore:    result.HasMore,
	})
}

func (h *Handler) respondError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, domain.ErrVideoNotFound):
		httpx.NotFound(c, err.Error())
	case errors.Is(err, domain.ErrInvalidTitle),
		errors.Is(err, domain.ErrInvalidMediaURL),
		errors.Is(err, domain.ErrInvalidCoverURL),
		errors.Is(err, domain.ErrInvalidCursor),
		errors.Is(err, domain.ErrInvalidIdempotency):
		httpx.BadRequest(c, err.Error())
	default:
		h.logger.Error("video request failed", "error", err)
		httpx.Internal(c, "服务内部错误")
	}
}

func toVideoResponse(video *domain.Video) *videoResponse {
	return &videoResponse{
		ID:          video.ID,
		AuthorID:    video.AuthorID,
		Title:       video.Title,
		MediaURL:    video.MediaURL,
		CoverURL:    video.CoverURL,
		PublishedAt: video.PublishedAt,
		CreatedAt:   video.CreatedAt,
	}
}
