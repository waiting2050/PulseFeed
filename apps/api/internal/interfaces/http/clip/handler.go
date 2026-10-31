// handler.go 实现 clip 发布、详情、列表与删除端点：参数校验、调用应用服务、领域错误映射。
package httpclip

import (
	"errors"
	"net/http"
	"strconv"

	appclip "pulsefeed/internal/application/clip"
	domclip "pulsefeed/internal/domain/clip"
	"pulsefeed/internal/interfaces/http/middleware"

	"github.com/gin-gonic/gin"
)

// Handler 聚合 clip 相关端点。
type Handler struct {
	service *appclip.Service
}

// New 创建 clip 处理器。
func New(service *appclip.Service) *Handler {
	return &Handler{service: service}
}

// Publish 处理 POST /api/clips，幂等键取自 Idempotency-Key 请求头，成功返回 201。
// 同一幂等键重复请求返回相同 clip_id（幂等回放）。
func (h *Handler) Publish(c *gin.Context) {
	creatorID, authed := middleware.UserIDFromContext(c)
	if !authed {
		fail(c, http.StatusUnauthorized, "missing or invalid token")
		return
	}
	var req publishRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "请求体非法")
		return
	}
	clip, err := h.service.CreatePublished(
		c.Request.Context(),
		creatorID,
		req.Title,
		req.VideoURL,
		req.CoverURL,
		c.GetHeader("Idempotency-Key"),
	)
	if err != nil {
		mapError(c, err)
		return
	}
	ok(c, http.StatusCreated, toClipResponse(clip))
}

// GetByID 处理 GET /api/clips/:clipId，返回 clip 详情。
func (h *Handler) GetByID(c *gin.Context) {
	clipID, err := strconv.ParseInt(c.Param("clipId"), 10, 64)
	if err != nil || clipID <= 0 {
		fail(c, http.StatusBadRequest, "非法的 clip ID")
		return
	}
	clip, err := h.service.Get(c.Request.Context(), clipID)
	if err != nil {
		mapError(c, err)
		return
	}
	ok(c, http.StatusOK, toClipResponse(clip))
}

// Delete 处理 DELETE /api/clips/:clipId，仅创作者本人可删除，成功返回 204。
func (h *Handler) Delete(c *gin.Context) {
	requesterID, ok := middleware.UserIDFromContext(c)
	if !ok {
		fail(c, http.StatusUnauthorized, "missing or invalid token")
		return
	}
	clipID, err := strconv.ParseInt(c.Param("clipId"), 10, 64)
	if err != nil || clipID <= 0 {
		fail(c, http.StatusBadRequest, "非法的 clip ID")
		return
	}
	if err := h.service.Delete(c.Request.Context(), requesterID, clipID, c.GetHeader("Idempotency-Key")); err != nil {
		mapError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// ListByCreator 处理 GET /api/users/:userId/clips，按游标分页返回创作者 clip 列表。
func (h *Handler) ListByCreator(c *gin.Context) {
	creatorID, err := strconv.ParseInt(c.Param("userId"), 10, 64)
	if err != nil || creatorID <= 0 {
		fail(c, http.StatusBadRequest, "非法的用户 ID")
		return
	}
	h.list(c, creatorID)
}

// ListMine 处理 GET /api/users/me/clips，按游标分页返回当前用户 clip 列表。
func (h *Handler) ListMine(c *gin.Context) {
	creatorID, ok := middleware.UserIDFromContext(c)
	if !ok {
		fail(c, http.StatusUnauthorized, "missing or invalid token")
		return
	}
	h.list(c, creatorID)
}

// list 是列表端点公共逻辑：解析 cursor/limit 查询参数并调用应用服务。
func (h *Handler) list(c *gin.Context, creatorID int64) {
	limit := 0
	if raw := c.Query("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			fail(c, http.StatusBadRequest, "非法的 limit")
			return
		}
		limit = parsed
	}
	result, err := h.service.ListByCreator(c.Request.Context(), creatorID, c.Query("cursor"), limit)
	if err != nil {
		mapError(c, err)
		return
	}

	items := make([]clipResponse, 0, len(result.Items))
	for _, clip := range result.Items {
		items = append(items, toClipResponse(clip))
	}
	ok(c, http.StatusOK, clipListResponse{
		Items:      items,
		NextCursor: result.NextCursor,
		HasMore:    result.HasMore,
	})
}

// toClipResponse 将领域实体转换为传输层响应。
func toClipResponse(clip *domclip.Clip) clipResponse {
	return clipResponse{
		ClipID:      clip.ID,
		CreatorID:   clip.CreatorID,
		Title:       clip.Title,
		VideoURL:    clip.VideoURL,
		CoverURL:    clip.CoverURL,
		PublishedAt: clip.PublishedAt,
	}
}

// mapError 将领域错误映射为 HTTP 状态码：400 参数非法、403 非创作者、404 不存在、409 冲突。
func mapError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, domclip.ErrInvalidTitle),
		errors.Is(err, domclip.ErrInvalidVideoURL),
		errors.Is(err, domclip.ErrInvalidIdempotencyKey),
		errors.Is(err, domclip.ErrInvalidCursor),
		errors.Is(err, domclip.ErrInvalidLimit):
		fail(c, http.StatusBadRequest, err.Error())
	case errors.Is(err, domclip.ErrNotClipOwner):
		fail(c, http.StatusForbidden, err.Error())
	case errors.Is(err, domclip.ErrClipNotFound):
		fail(c, http.StatusNotFound, err.Error())
	case errors.Is(err, domclip.ErrClipExists):
		fail(c, http.StatusConflict, err.Error())
	default:
		fail(c, http.StatusInternalServerError, "内部错误")
	}
}

// ok 以统一信封返回成功响应。
func ok(c *gin.Context, status int, data any) {
	c.JSON(status, gin.H{"code": 0, "message": "ok", "data": data})
}

// fail 以统一信封返回错误响应，code 与 HTTP 状态码保持一致。
func fail(c *gin.Context, status int, message string) {
	c.JSON(status, gin.H{"code": status, "message": message})
}
