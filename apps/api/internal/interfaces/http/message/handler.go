// handler.go 实现站内消息端点：列表、未读数、全部标记已读以及 internal 主动投递。
package httpmsg

import (
	"net/http"
	"strconv"
	"time"

	appmessage "pulsefeed/internal/application/message"
	dommessage "pulsefeed/internal/domain/message"
	"pulsefeed/internal/interfaces/http/middleware"

	"github.com/gin-gonic/gin"
)

// Handler 聚合消息相关端点。
type Handler struct {
	service *appmessage.Service
}

// New 创建消息处理器。
func New(service *appmessage.Service) *Handler {
	return &Handler{service: service}
}

// ListMessages 处理 GET /api/messages，按游标分页返回已登录用户的站内消息。
func (h *Handler) ListMessages(c *gin.Context) {
	viewerID, authed := middleware.UserIDFromContext(c)
	if !authed {
		fail(c, http.StatusUnauthorized, "missing or invalid token")
		return
	}
	limit := parseLimit(c.Query("limit"))
	result, err := h.service.ListPage(c.Request.Context(), viewerID, c.Query("cursor"), limit)
	if err != nil {
		mapError(c, err)
		return
	}
	items := make([]messageResponse, 0, len(result.Items))
	for _, m := range result.Items {
		items = append(items, messageResponse{
			ID:        m.ID,
			ActorID:   m.ActorID,
			Kind:      string(m.Kind),
			TargetID:  m.TargetID,
			EventID:   m.EventID,
			Read:      m.IsRead(),
			ReadAt:    messageReadAt(m),
			CreatedAt: m.CreatedAt,
		})
	}
	respondOK(c, http.StatusOK, listResponse{Items: items, NextCursor: result.NextCursor, HasMore: result.HasMore})
}

// GetUnreadCount 处理 GET /api/message-stats/unread，返回已登录用户未读数。
func (h *Handler) GetUnreadCount(c *gin.Context) {
	viewerID, authed := middleware.UserIDFromContext(c)
	if !authed {
		fail(c, http.StatusUnauthorized, "missing or invalid token")
		return
	}
	n, err := h.service.CountUnread(c.Request.Context(), viewerID)
	if err != nil {
		mapError(c, err)
		return
	}
	respondOK(c, http.StatusOK, unreadResponse{Unread: n})
}

// MarkAllRead 处理 PATCH /api/messages，把当前用户所有未读消息一次性标记已读。
func (h *Handler) MarkAllRead(c *gin.Context) {
	viewerID, authed := middleware.UserIDFromContext(c)
	if !authed {
		fail(c, http.StatusUnauthorized, "missing or invalid token")
		return
	}
	if err := h.service.MarkAllRead(c.Request.Context(), viewerID); err != nil {
		mapError(c, err)
		return
	}
	respondOK(c, http.StatusOK, gin.H{"marked": true})
}

// CreateMessageInternal 处理 POST /internal/messages（X-Internal-Token），主动投递站内消息。
func (h *Handler) CreateMessageInternal(c *gin.Context) {
	var req createRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "请求体非法")
		return
	}
	inserted, err := h.service.CreateFromActorEvent(
		c.Request.Context(),
		req.ReceiverID, req.ActorID, req.TargetID,
		dommessage.Kind(req.Kind), req.EventID,
	)
	if err != nil {
		mapError(c, err)
		return
	}
	respondOK(c, http.StatusCreated, gin.H{"inserted": inserted})
}

// parseLimit 把查询参数 limit 解析为整数；空串或解析失败返回 0（默认）。
func parseLimit(raw string) int {
	if raw == "" {
		return 0
	}
	v, _ := strconv.Atoi(raw)
	return v
}

// messageReadAt 把领域消息的 ReadAt 转为可空时间指针（已读才返回）。
func messageReadAt(m *dommessage.Message) *time.Time {
	if m == nil || m.ReadAt.IsZero() {
		return nil
	}
	t := m.ReadAt
	return &t
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
