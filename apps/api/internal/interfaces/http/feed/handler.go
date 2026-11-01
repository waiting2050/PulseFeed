// handler.go 实现 Feed 读取端点：参数解析、调用应用服务、领域错误映射。
package httpfeed

import (
	"errors"
	"net/http"
	"strconv"

	appfeed "pulsefeed/internal/application/feed"
	domfeed "pulsefeed/internal/domain/feed"
	"pulsefeed/internal/interfaces/http/middleware"

	"github.com/gin-gonic/gin"
)

// Handler 聚合 feed 相关端点。
type Handler struct {
	service *appfeed.Service
}

// New 创建 feed 处理器。
func New(service *appfeed.Service) *Handler {
	return &Handler{service: service}
}

// GetFeed 处理 GET /api/feed?scene=&cursor=&limit=，reader 可为未登录（可选 JWT）。
func (h *Handler) GetFeed(c *gin.Context) {
	limit := 0
	if raw := c.Query("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			fail(c, http.StatusBadRequest, "非法的 limit")
			return
		}
		limit = parsed
	}
	h.serve(c, &appfeed.FeedRequest{
		Scene:  c.Query("scene"),
		Cursor: c.Query("cursor"),
		Limit:  limit,
	})
}

// SearchFeed 处理 POST /api/feed-search，请求体与查询参数同义，供前端以 POST 方式翻页。
func (h *Handler) SearchFeed(c *gin.Context) {
	var req feedSearchRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "请求体非法")
		return
	}
	h.serve(c, &appfeed.FeedRequest{
		Scene:  req.Scene,
		Cursor: req.Cursor,
		Limit:  req.Limit,
	})
}

// serve 是两个端点的公共逻辑：附带读者身份调用应用服务并输出分页响应。
func (h *Handler) serve(c *gin.Context, req *appfeed.FeedRequest) {
	viewerID, _ := middleware.UserIDFromContext(c)
	req.ViewerID = viewerID

	result, err := h.service.GetFeed(c.Request.Context(), req)
	if err != nil {
		mapError(c, err)
		return
	}

	items := make([]feedEntryResponse, 0, len(result.Items))
	for _, entry := range result.Items {
		items = append(items, toFeedEntryResponse(entry))
	}
	ok(c, http.StatusOK, feedResponse{
		Items:      items,
		NextCursor: result.NextCursor,
		HasMore:    result.HasMore,
	})
}

// toFeedEntryResponse 将领域条目转换为传输层响应。
func toFeedEntryResponse(entry *domfeed.FeedEntry) feedEntryResponse {
	return feedEntryResponse{
		ClipID:       entry.ClipID,
		CreatorID:    entry.CreatorID,
		Title:        entry.Title,
		VideoURL:     entry.VideoURL,
		CoverURL:     entry.CoverURL,
		PublishedAt:  entry.PublishedAt,
		LikeCount:    entry.Stats.LikeCount,
		SaveCount:    entry.Stats.SaveCount,
		CommentCount: entry.Stats.CommentCount,
		PlayCount:    entry.Stats.PlayCount,
		Liked:        entry.Viewer.Liked,
		Saved:        entry.Viewer.Saved,
	}
}

// mapError 将领域错误映射为 HTTP 状态码：400 参数/场景非法、401 要求登录。
func mapError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, domfeed.ErrUnsupportedScene),
		errors.Is(err, domfeed.ErrInvalidCursor),
		errors.Is(err, domfeed.ErrInvalidLimit):
		fail(c, http.StatusBadRequest, err.Error())
	case errors.Is(err, domfeed.ErrViewerRequired):
		fail(c, http.StatusUnauthorized, err.Error())
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
