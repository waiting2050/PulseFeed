// handler.go 实现曝光端点：播放事件上报，并映射领域错误到 HTTP 码。
package httpimpression

import (
	"errors"
	"net/http"

	appimpression "pulsefeed/internal/application/impression"
	domimpression "pulsefeed/internal/domain/impression"
	"pulsefeed/internal/interfaces/http/middleware"

	"github.com/gin-gonic/gin"
)

// Handler 聚合曝光相关端点。
type Handler struct {
	service *appimpression.Service
}

// New 创建曝光处理器。
func New(service *appimpression.Service) *Handler {
	return &Handler{service: service}
}

// RecordPlayEvent 处理 POST /api/clip-play-events：登录用户上报一次展示/播放行为。
func (h *Handler) RecordPlayEvent(c *gin.Context) {
	userID, ok := middleware.UserIDFromContext(c)
	if !ok {
		fail(c, http.StatusUnauthorized, "缺少登录身份")
		return
	}

	var req recordPlayEventRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "请求体非法")
		return
	}

	// watch_ms 为当前字段；旧客户端只传 play_duration_ms，非零时作为兼容回退。
	watchMs := req.WatchMs
	if watchMs == 0 && req.PlayDurationMs > 0 {
		watchMs = req.PlayDurationMs
	}

	err := h.service.RecordPlayEvent(c.Request.Context(), domimpression.PlayEventInput{
		EventID:    req.EventID,
		ViewerID:   userID,
		ClipID:     req.ClipID,
		EventType:  req.EventType,
		Scene:      req.Scene,
		RequestID:  req.RequestID,
		WatchMs:    watchMs,
		Completed:  req.Completed,
		Impression: req.Impression,
	})
	if err != nil {
		mapError(c, err)
		return
	}
	okEnvelope(c, http.StatusOK, recordPlayEventResponse{EventID: req.EventID})
}

// mapError 将领域错误映射为 HTTP 状态码。
func mapError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, domimpression.ErrInvalidEventID),
		errors.Is(err, domimpression.ErrInvalidEventType),
		errors.Is(err, domimpression.ErrInvalidScene),
		errors.Is(err, domimpression.ErrInvalidRequestID),
		errors.Is(err, domimpression.ErrInvalidPlayDuration):
		fail(c, http.StatusBadRequest, err.Error())
	case errors.Is(err, domimpression.ErrClipNotFound):
		fail(c, http.StatusNotFound, err.Error())
	default:
		fail(c, http.StatusInternalServerError, "内部错误")
	}
}

// okEnvelope 以统一信封返回成功响应。
func okEnvelope(c *gin.Context, status int, data any) {
	c.JSON(status, gin.H{"code": 0, "message": "ok", "data": data})
}

// fail 以统一信封返回错误响应，code 与 HTTP 状态码保持一致。
func fail(c *gin.Context, status int, message string) {
	c.JSON(status, gin.H{"code": status, "message": message})
}
