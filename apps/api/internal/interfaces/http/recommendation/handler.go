// handler.go 实现 internal 推荐与曝光端点：内部 token 鉴权、参数校验、错误映射。
package httprecommendation

import (
	"errors"
	"net/http"
	"strconv"

	apprecommendation "pulsefeed/internal/application/recommendation"
	domrecommendation "pulsefeed/internal/domain/recommendation"

	"github.com/gin-gonic/gin"
)

// defaultCandidateLimit 是内部候选召回的默认条数（C5 候选池下限）。
const defaultCandidateLimit = domrecommendation.CandidatePoolMin

// defaultImpressionDays 是内部曝光查询的默认天数窗口。
const defaultImpressionDays = 7

// defaultImpressionLimit 是内部曝光查询的默认条数。
const defaultImpressionLimit = 50

// Handler 聚合 internal 推荐/曝光端点。
type Handler struct {
	service *apprecommendation.Service
}

// New 创建 internal 推荐处理器。
func New(service *apprecommendation.Service) *Handler {
	return &Handler{service: service}
}

// ListCandidates 处理 POST /internal/recommendation-candidates：
// 按当前时刻快照召回候选池，返回热度分与是否已生成向量。
func (h *Handler) ListCandidates(c *gin.Context) {
	var req candidateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "请求体非法")
		return
	}
	limit := req.Limit
	if limit == 0 {
		limit = defaultCandidateLimit
	}

	views, err := h.service.ListCandidates(c.Request.Context(), req.ViewerID, limit)
	if err != nil {
		mapError(c, err)
		return
	}

	items := make([]candidateResponse, 0, len(views))
	for _, view := range views {
		items = append(items, candidateResponse{
			ClipID:       view.ClipID,
			CreatorID:    view.CreatorID,
			PublishedAt:  view.PublishedAt,
			HotScore:     view.HotScore,
			HasEmbedding: view.HasEmbedding,
		})
	}
	ok(c, http.StatusOK, candidateListResponse{ViewerID: req.ViewerID, Items: items})
}

// ImpressionDecision 处理 GET /internal/impression-decisions?viewer_id=&clip_id=，
// 返回该 clip 是否仍在 7 天曝光排除窗口内。
func (h *Handler) ImpressionDecision(c *gin.Context) {
	viewerID, err := parsePositiveInt64(c.Query("viewer_id"))
	if err != nil {
		fail(c, http.StatusBadRequest, "非法的 viewer_id")
		return
	}
	clipID, err := parsePositiveInt64(c.Query("clip_id"))
	if err != nil {
		fail(c, http.StatusBadRequest, "非法的 clip_id")
		return
	}

	decision, err := h.service.ImpressionDecision(c.Request.Context(), viewerID, clipID)
	if err != nil {
		mapError(c, err)
		return
	}
	ok(c, http.StatusOK, impressionDecisionResponse{
		ViewerID:     viewerID,
		ClipID:       clipID,
		ShouldShow:   decision.ShouldShow,
		Reason:       decision.Reason,
		LastPlayedAt: decision.LastPlayedAt,
	})
}

// ListImpressions 处理 GET /internal/impressions?viewer_id=&days=&limit=，
// 返回读者最近曝光聚合记录，供推荐排查与运营核对。
func (h *Handler) ListImpressions(c *gin.Context) {
	viewerID, err := parsePositiveInt64(c.Query("viewer_id"))
	if err != nil {
		fail(c, http.StatusBadRequest, "非法的 viewer_id")
		return
	}

	days := defaultImpressionDays
	if raw := c.Query("days"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			fail(c, http.StatusBadRequest, "非法的 days")
			return
		}
		days = parsed
	}
	limit := defaultImpressionLimit
	if raw := c.Query("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			fail(c, http.StatusBadRequest, "非法的 limit")
			return
		}
		limit = parsed
	}

	records, err := h.service.ListImpressions(c.Request.Context(), viewerID, days, limit)
	if err != nil {
		mapError(c, err)
		return
	}

	items := make([]impressionResponse, 0, len(records))
	for _, record := range records {
		items = append(items, impressionResponse{
			ViewerID:       record.ViewerID,
			ClipID:         record.ClipID,
			Day:            record.Day,
			PlayCount:      record.PlayCount,
			CompletedCount: record.CompletedCount,
			FirstPlayedAt:  record.FirstPlayedAt,
			LastPlayedAt:   record.LastPlayedAt,
		})
	}
	ok(c, http.StatusOK, impressionListResponse{ViewerID: viewerID, Days: days, Items: items})
}

// parsePositiveInt64 解析正整数路径/查询参数；空值、非数字或非正数返回错误。
func parsePositiveInt64(raw string) (int64, error) {
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value <= 0 {
		return 0, errors.New("非法的正整数参数")
	}
	return value, nil
}

// mapError 将领域错误映射为 HTTP 状态码：400 参数/身份非法、404 曝光记录不存在。
func mapError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, domrecommendation.ErrViewerRequired),
		errors.Is(err, domrecommendation.ErrInvalidLimit),
		errors.Is(err, domrecommendation.ErrInvalidCursor):
		fail(c, http.StatusBadRequest, err.Error())
	case errors.Is(err, domrecommendation.ErrImpressionNotFound):
		fail(c, http.StatusNotFound, err.Error())
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
