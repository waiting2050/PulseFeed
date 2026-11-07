// handler.go 实现播放配置与 QoS 上报端点：公开 + internal 路由。
package httpplayback

import (
	"net/http"

	appplayback "pulsefeed/internal/application/playback"
	domplayback "pulsefeed/internal/domain/playback"
	"pulsefeed/internal/interfaces/http/middleware"

	"github.com/gin-gonic/gin"
)

// Handler 聚合 playback 相关端点。
type Handler struct {
	service *appplayback.Service
}

// New 创建 playback 处理器。
func New(service *appplayback.Service) *Handler {
	return &Handler{service: service}
}

// GetConfig 处理 GET /api/playback-config，返回当前用户播放配置（未命中返回默认值）。
func (h *Handler) GetConfig(c *gin.Context) {
	viewerID, authed := middleware.UserIDFromContext(c)
	if !authed {
		fail(c, http.StatusUnauthorized, "missing or invalid token")
		return
	}
	cfg, err := h.service.GetConfig(c.Request.Context(), viewerID)
	if err != nil {
		mapError(c, err)
		return
	}
	respondOK(c, http.StatusOK, configResponse{
		PreloadCount:  cfg.PreloadCount,
		QoSSampleRate: cfg.QoSSampleRate,
		BufferMS:      cfg.BufferMS,
	})
}

// UpdateConfig 处理 PUT /api/playback-config，按请求体字段覆盖播放配置；缺省字段继承现有值。
func (h *Handler) UpdateConfig(c *gin.Context) {
	viewerID, authed := middleware.UserIDFromContext(c)
	if !authed {
		fail(c, http.StatusUnauthorized, "missing or invalid token")
		return
	}
	var req configUpdateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "请求体非法")
		return
	}
	cfg, err := h.service.UpdateConfig(c.Request.Context(), viewerID, req.PreloadCount, req.QoSSampleRate, req.BufferMS)
	if err != nil {
		mapError(c, err)
		return
	}
	respondOK(c, http.StatusOK, configResponse{
		PreloadCount:  cfg.PreloadCount,
		QoSSampleRate: cfg.QoSSampleRate,
		BufferMS:      cfg.BufferMS,
	})
}

// GetPreload 处理 GET /api/preload-clips，返回按当前用户配置预加载的媒体列表。
func (h *Handler) GetPreload(c *gin.Context) {
	viewerID, authed := middleware.UserIDFromContext(c)
	if !authed {
		fail(c, http.StatusUnauthorized, "missing or invalid token")
		return
	}
	items, err := h.service.GetPreload(c.Request.Context(), viewerID)
	if err != nil {
		mapError(c, err)
		return
	}
	response := preloadResponse{Items: make([]preloadItem, 0, len(items))}
	for _, item := range items {
		response.Items = append(response.Items, preloadItem{
			ClipID:     item.ClipID,
			MediaURL:   item.MediaURL,
			CoverURL:   item.CoverURL,
			DurationMS: item.DurationMS,
		})
	}
	respondOK(c, http.StatusOK, response)
}

// ReportQoS 处理 POST /api/playback-qos-reports（登录），幂等上报。
func (h *Handler) ReportQoS(c *gin.Context) {
	viewerID, authed := middleware.UserIDFromContext(c)
	if !authed {
		fail(c, http.StatusUnauthorized, "missing or invalid token")
		return
	}
	var req qosReportRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "请求体非法")
		return
	}
	inserted, err := h.service.ReportQoS(c.Request.Context(), viewerID, &domplayback.QoSReport{
		ClipID:     req.ClipID,
		EventID:    req.EventID,
		LatencyMS:  req.LatencyMS,
		StallCount: req.StallCount,
		Completed:  req.Completed,
		ClientInfo: req.ClientInfo,
	})
	if err != nil {
		mapError(c, err)
		return
	}
	respondOK(c, http.StatusCreated, qosReportResponse{Inserted: inserted})
}

// ReportQoSInternal 处理 POST /internal/playback-qos-reports（X-Internal-Token），允许指定 reporter。
func (h *Handler) ReportQoSInternal(c *gin.Context) {
	var req qosReportRequestWithReporter
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "请求体非法")
		return
	}
	inserted, err := h.service.ReportQoS(c.Request.Context(), req.ReporterID, &domplayback.QoSReport{
		ClipID:     req.ClipID,
		EventID:    req.EventID,
		LatencyMS:  req.LatencyMS,
		StallCount: req.StallCount,
		Completed:  req.Completed,
		ClientInfo: req.ClientInfo,
	})
	if err != nil {
		mapError(c, err)
		return
	}
	respondOK(c, http.StatusCreated, qosReportResponse{Inserted: inserted})
}

// qosReportRequestWithReporter 是 internal 路由的请求体，允许显式指定 reporter。
type qosReportRequestWithReporter struct {
	ReporterID int64  `json:"reporter_user_id"`
	ClipID     int64  `json:"clip_id"`
	EventID    string `json:"event_id"`
	LatencyMS  int    `json:"latency_ms"`
	StallCount int    `json:"stall_count"`
	Completed  bool   `json:"completed"`
	ClientInfo string `json:"client_info,omitempty"`
}

// respondOK 统一信封返回成功。
func respondOK(c *gin.Context, status int, data any) {
	c.JSON(status, gin.H{"code": 0, "message": "ok", "data": data})
}

// fail 统一信封返回错误。
func fail(c *gin.Context, status int, message string) {
	c.JSON(status, gin.H{"code": status, "message": message})
}

// mapError 把领域错误映射为 HTTP 状态码。
func mapError(c *gin.Context, err error) {
	switch {
	case err == nil:
		return
	default:
		fail(c, http.StatusInternalServerError, "内部错误")
	}
}
