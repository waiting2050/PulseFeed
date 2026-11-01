// handler.go 实现互动端点：点赞/收藏状态变更与评论增删查，并映射领域错误到 HTTP 码。
package httpaction

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	appaction "pulsefeed/internal/application/interaction"
	dominteraction "pulsefeed/internal/domain/interaction"
	"pulsefeed/internal/interfaces/http/middleware"

	"github.com/gin-gonic/gin"
)

// idempotencyKeyMaxLen 是幂等键长度上限，与 pf_actions/pf_comments 列宽一致。
const idempotencyKeyMaxLen = 128

// Handler 聚合互动相关端点。
type Handler struct {
	service *appaction.Service
}

// New 创建互动处理器。
func New(service *appaction.Service) *Handler {
	return &Handler{service: service}
}

// Like 处理 PUT /api/clips/:clipId/like。
func (h *Handler) Like(c *gin.Context) {
	h.changeAction(c, dominteraction.ActionLike, true)
}

// Unlike 处理 DELETE /api/clips/:clipId/like。
func (h *Handler) Unlike(c *gin.Context) {
	h.changeAction(c, dominteraction.ActionLike, false)
}

// Save 处理 PUT /api/clips/:clipId/save。
func (h *Handler) Save(c *gin.Context) {
	h.changeAction(c, dominteraction.ActionSave, true)
}

// Unsave 处理 DELETE /api/clips/:clipId/save。
func (h *Handler) Unsave(c *gin.Context) {
	h.changeAction(c, dominteraction.ActionSave, false)
}

// changeAction 是四类状态变更端点的公共逻辑。
func (h *Handler) changeAction(c *gin.Context, actionType dominteraction.ActionType, active bool) {
	userID, ok := middleware.UserIDFromContext(c)
	if !ok {
		fail(c, http.StatusUnauthorized, "缺少登录身份")
		return
	}
	clipID, ok := parseParamID(c, "clipId")
	if !ok {
		return
	}
	key, ok := idempotencyKey(c)
	if !ok {
		return
	}

	result, err := h.serviceAction(c, userID, clipID, actionType, active, key)
	if err != nil {
		mapError(c, err)
		return
	}

	if actionType == dominteraction.ActionLike {
		okEnvelope(c, http.StatusOK, likeActionResponse{
			ClipID:     result.ClipID,
			ActionType: string(result.ActionType),
			Active:     result.Active,
			LikeCount:  result.LikeCount,
		})
		return
	}
	okEnvelope(c, http.StatusOK, saveActionResponse{
		ClipID:     result.ClipID,
		ActionType: string(result.ActionType),
		Active:     result.Active,
		SaveCount:  result.SaveCount,
	})
}

// serviceAction 按行为类型分发到应用服务，避免在 handler 里重复写四个分支的取值逻辑。
func (h *Handler) serviceAction(c *gin.Context, userID, clipID int64, actionType dominteraction.ActionType, active bool, key string) (*dominteraction.ActionResult, error) {
	ctx := c.Request.Context()
	switch {
	case actionType == dominteraction.ActionLike && active:
		return h.service.Like(ctx, userID, clipID, key)
	case actionType == dominteraction.ActionLike:
		return h.service.Unlike(ctx, userID, clipID, key)
	case active:
		return h.service.Save(ctx, userID, clipID, key)
	default:
		return h.service.Unsave(ctx, userID, clipID, key)
	}
}

// CreateComment 处理 POST /api/clips/:clipId/comments。
func (h *Handler) CreateComment(c *gin.Context) {
	userID, ok := middleware.UserIDFromContext(c)
	if !ok {
		fail(c, http.StatusUnauthorized, "缺少登录身份")
		return
	}
	clipID, ok := parseParamID(c, "clipId")
	if !ok {
		return
	}
	key, ok := idempotencyKey(c)
	if !ok {
		return
	}

	var req createCommentRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "请求体非法")
		return
	}

	comment, count, err := h.service.CreateComment(c.Request.Context(), userID, clipID, req.Content, key)
	if err != nil {
		mapError(c, err)
		return
	}
	okEnvelope(c, http.StatusCreated, createCommentResponse{
		commentResponse: toCommentResponse(comment),
		CommentCount:    count,
	})
}

// ListComments 处理 GET /api/clips/:clipId/comments（可匿名）。
func (h *Handler) ListComments(c *gin.Context) {
	clipID, ok := parseParamID(c, "clipId")
	if !ok {
		return
	}
	limit := 0
	if raw := c.Query("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			fail(c, http.StatusBadRequest, "非法的 limit")
			return
		}
		limit = parsed
	}

	result, err := h.service.ListComments(c.Request.Context(), clipID, c.Query("cursor"), limit)
	if err != nil {
		mapError(c, err)
		return
	}
	items := make([]commentResponse, 0, len(result.Items))
	for _, comment := range result.Items {
		items = append(items, toCommentResponse(comment))
	}
	okEnvelope(c, http.StatusOK, commentListResponse{
		Items:      items,
		NextCursor: result.NextCursor,
		HasMore:    result.HasMore,
	})
}

// DeleteComment 处理 DELETE /api/comments/:commentId。
func (h *Handler) DeleteComment(c *gin.Context) {
	userID, ok := middleware.UserIDFromContext(c)
	if !ok {
		fail(c, http.StatusUnauthorized, "缺少登录身份")
		return
	}
	commentID, ok := parseParamID(c, "commentId")
	if !ok {
		return
	}

	result, err := h.service.DeleteComment(c.Request.Context(), userID, commentID)
	if err != nil {
		mapError(c, err)
		return
	}
	okEnvelope(c, http.StatusOK, deleteCommentResponse{
		CommentID:    result.CommentID,
		Status:       result.Status,
		CommentCount: result.CommentCount,
	})
}

// toCommentResponse 将领域评论转换为传输层响应。
func toCommentResponse(comment *dominteraction.Comment) commentResponse {
	return commentResponse{
		ID:        comment.ID,
		ClipID:    comment.ClipID,
		UserID:    comment.UserID,
		Nickname:  comment.Nickname,
		AvatarURL: comment.AvatarURL,
		Content:   comment.Content,
		CreatedAt: comment.CreatedAt,
	}
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

// mapError 将领域错误映射为 HTTP 状态码：400 参数非法、403 权限不足、404 资源缺失。
func mapError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, dominteraction.ErrInvalidActionType),
		errors.Is(err, dominteraction.ErrInvalidCommentContent),
		errors.Is(err, dominteraction.ErrInvalidCursor),
		errors.Is(err, dominteraction.ErrInvalidLimit):
		fail(c, http.StatusBadRequest, err.Error())
	case errors.Is(err, dominteraction.ErrCommentPermissionDenied):
		fail(c, http.StatusForbidden, err.Error())
	case errors.Is(err, dominteraction.ErrClipNotFound),
		errors.Is(err, dominteraction.ErrCommentNotFound):
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
