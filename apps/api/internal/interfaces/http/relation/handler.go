// handler.go 实现关注关系端点：关注/取关状态变更与关注/粉丝列表查询，并映射领域错误到 HTTP 码。
package httprelation

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	apprelation "pulsefeed/internal/application/relation"
	domrelation "pulsefeed/internal/domain/relation"
	"pulsefeed/internal/interfaces/http/middleware"

	"github.com/gin-gonic/gin"
)

// idempotencyKeyMaxLen 是幂等键长度上限，与 pf_follows 列宽一致。
const idempotencyKeyMaxLen = 128

// Handler 聚合 relation 相关端点。
type Handler struct {
	service *apprelation.Service
}

// New 创建 relation 处理器。
func New(service *apprelation.Service) *Handler {
	return &Handler{service: service}
}

// Follow 处理 PUT /api/users/me/following/:targetUserId。
func (h *Handler) Follow(c *gin.Context) {
	h.setFollow(c, true)
}

// Unfollow 处理 DELETE /api/users/me/following/:targetUserId。
func (h *Handler) Unfollow(c *gin.Context) {
	h.setFollow(c, false)
}

// setFollow 是关注/取关端点的公共逻辑：鉴权、参数解析、调用应用服务并输出回执。
func (h *Handler) setFollow(c *gin.Context, follow bool) {
	userID, ok := middleware.UserIDFromContext(c)
	if !ok {
		fail(c, http.StatusUnauthorized, "缺少登录身份")
		return
	}
	targetUserID, ok := parseParamID(c, "targetUserId")
	if !ok {
		return
	}
	key, ok := idempotencyKey(c)
	if !ok {
		return
	}

	var result *domrelation.FollowResult
	var err error
	if follow {
		result, err = h.service.Follow(c.Request.Context(), userID, targetUserID, key)
	} else {
		result, err = h.service.Unfollow(c.Request.Context(), userID, targetUserID, key)
	}
	if err != nil {
		mapError(c, err)
		return
	}
	okEnvelope(c, http.StatusOK, followResponse{
		UserID:         result.UserID,
		TargetUserID:   result.TargetUserID,
		Status:         result.Status,
		Following:      result.Following,
		FollowingCount: result.FollowingCount,
		FollowerCount:  result.FollowerCount,
	})
}

// GetStats 处理 GET /api/users/:userId/relation：返回公开的计数与当前读者关注状态。
func (h *Handler) GetStats(c *gin.Context) {
	targetUserID, ok := parseParamID(c, "userId")
	if !ok {
		return
	}
	viewerID, _ := middleware.UserIDFromContext(c)
	result, err := h.service.Stats(c.Request.Context(), viewerID, targetUserID)
	if err != nil {
		mapError(c, err)
		return
	}
	okEnvelope(c, http.StatusOK, relationStatsResponse{
		UserID:         targetUserID,
		FollowingCount: result.FollowingCount,
		FollowerCount:  result.FollowerCount,
		Following:      result.Following,
		IsSelf:         viewerID == targetUserID,
	})
}

// ListFollowing 处理 GET /api/users/me/following。
func (h *Handler) ListFollowing(c *gin.Context) {
	userID, ok := middleware.UserIDFromContext(c)
	if !ok {
		fail(c, http.StatusUnauthorized, "缺少登录身份")
		return
	}
	h.list(c, userID, true)
}

// ListFollowers 处理 GET /api/users/me/followers。
func (h *Handler) ListFollowers(c *gin.Context) {
	userID, ok := middleware.UserIDFromContext(c)
	if !ok {
		fail(c, http.StatusUnauthorized, "缺少登录身份")
		return
	}
	h.list(c, userID, false)
}

// ListFollowingOf 处理 GET /api/users/:userId/following：公开查看任意用户的关注列表。
func (h *Handler) ListFollowingOf(c *gin.Context) {
	userID, ok := parseParamID(c, "userId")
	if !ok {
		return
	}
	h.list(c, userID, true)
}

// ListFollowersOf 处理 GET /api/users/:userId/followers：公开查看任意用户的粉丝列表。
func (h *Handler) ListFollowersOf(c *gin.Context) {
	userID, ok := parseParamID(c, "userId")
	if !ok {
		return
	}
	h.list(c, userID, false)
}

// list 是关注/粉丝列表端点的公共逻辑。
func (h *Handler) list(c *gin.Context, userID int64, following bool) {
	limit := 0
	if raw := c.Query("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			fail(c, http.StatusBadRequest, "非法的 limit")
			return
		}
		limit = parsed
	}

	var result *apprelation.ListResult
	var err error
	if following {
		result, err = h.service.ListFollowing(c.Request.Context(), userID, c.Query("cursor"), limit)
	} else {
		result, err = h.service.ListFollowers(c.Request.Context(), userID, c.Query("cursor"), limit)
	}
	if err != nil {
		mapError(c, err)
		return
	}
	items := make([]relationEntryResponse, 0, len(result.Items))
	for _, entry := range result.Items {
		items = append(items, relationEntryResponse{
			UserID:     entry.UserID,
			Nickname:   entry.Nickname,
			AvatarURL:  entry.AvatarURL,
			Bio:        entry.Bio,
			FollowedAt: entry.FollowedAt,
		})
	}
	okEnvelope(c, http.StatusOK, relationListResponse{
		Items:      items,
		NextCursor: result.NextCursor,
		HasMore:    result.HasMore,
	})
}

// parseParamID 解析路径参数中的正整数 ID；非法时直接返回 400。
func parseParamID(c *gin.Context, name string) (int64, bool) {
	id, err := strconv.ParseInt(c.Param(name), 10, 64)
	if err != nil || id <= 0 {
		fail(c, http.StatusBadRequest, "非法的 "+name)
		return 0, false
	}
	return id, true
}

// idempotencyKey 读取并校验 Idempotency-Key 头；缺省允许（为空串），超长返回 400。
func idempotencyKey(c *gin.Context) (string, bool) {
	key := strings.TrimSpace(c.GetHeader("Idempotency-Key"))
	if len(key) > idempotencyKeyMaxLen {
		fail(c, http.StatusBadRequest, "Idempotency-Key 超长")
		return "", false
	}
	return key, true
}

// mapError 将领域错误映射为 HTTP 状态码：400 参数/自关注非法、404 目标用户不存在。
func mapError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, domrelation.ErrFollowSelf),
		errors.Is(err, domrelation.ErrInvalidCursor),
		errors.Is(err, domrelation.ErrInvalidLimit):
		fail(c, http.StatusBadRequest, err.Error())
	case errors.Is(err, domrelation.ErrTargetNotFound):
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
