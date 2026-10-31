// handler.go 实现账号认证相关 HTTP 端点：参数校验、调用应用服务、领域错误到状态码的映射。
package httpaccount

import (
	"errors"
	"net/http"
	"strconv"

	appaccount "pulsefeed/internal/application/account"
	domainaccount "pulsefeed/internal/domain/account"
	"pulsefeed/internal/interfaces/http/middleware"

	"github.com/gin-gonic/gin"
)

// Handler 聚合账号相关端点。
type Handler struct {
	service *appaccount.Service
}

// New 创建账号处理器。
func New(service *appaccount.Service) *Handler {
	return &Handler{service: service}
}

// Register 处理 POST /api/users，成功返回 201 与资料视图。
func (h *Handler) Register(c *gin.Context) {
	var req registerRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "请求体非法")
		return
	}
	profile, err := h.service.Register(c.Request.Context(), req.Account, req.Password, req.Nickname)
	if err != nil {
		mapError(c, err)
		return
	}
	ok(c, http.StatusCreated, toProfileResponse(profile))
}

// Login 处理 POST /api/sessions，成功返回 200 与访问令牌。
func (h *Handler) Login(c *gin.Context) {
	var req loginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "请求体非法")
		return
	}
	session, err := h.service.Login(c.Request.Context(), req.Account, req.Password)
	if err != nil {
		mapError(c, err)
		return
	}
	ok(c, http.StatusOK, sessionResponse{
		AccessToken: session.AccessToken,
		TokenType:   "Bearer",
		ExpiresIn:   session.ExpiresIn,
		UserID:      session.UserID,
		Account:     session.Account,
	})
}

// Logout 处理 DELETE /api/sessions/current，无状态登出返回 204。
func (h *Handler) Logout(c *gin.Context) {
	userID, authed := middleware.UserIDFromContext(c)
	if !authed {
		fail(c, http.StatusUnauthorized, "missing or invalid token")
		return
	}
	if err := h.service.Logout(c.Request.Context(), userID); err != nil {
		mapError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// Me 处理 GET /api/users/me，返回当前用户资料。
func (h *Handler) Me(c *gin.Context) {
	userID, authed := middleware.UserIDFromContext(c)
	if !authed {
		fail(c, http.StatusUnauthorized, "missing or invalid token")
		return
	}
	profile, err := h.service.Me(c.Request.Context(), userID)
	if err != nil {
		mapError(c, err)
		return
	}
	ok(c, http.StatusOK, toProfileResponse(profile))
}

// UpdateMe 处理 PATCH /api/users/me，部分更新当前用户资料。
func (h *Handler) UpdateMe(c *gin.Context) {
	userID, authed := middleware.UserIDFromContext(c)
	if !authed {
		fail(c, http.StatusUnauthorized, "missing or invalid token")
		return
	}
	var req updateProfileRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "请求体非法")
		return
	}
	profile, err := h.service.UpdateMe(c.Request.Context(), userID, req.Nickname, req.AvatarURL, req.Bio)
	if err != nil {
		mapError(c, err)
		return
	}
	ok(c, http.StatusOK, toProfileResponse(profile))
}

// GetByID 处理 GET /api/users/:userId，按用户 ID 查询公开资料。
func (h *Handler) GetByID(c *gin.Context) {
	userID, err := strconv.ParseInt(c.Param("userId"), 10, 64)
	if err != nil || userID <= 0 {
		fail(c, http.StatusBadRequest, "非法的用户 ID")
		return
	}
	profile, err := h.service.Me(c.Request.Context(), userID)
	if err != nil {
		mapError(c, err)
		return
	}
	ok(c, http.StatusOK, toProfileResponse(profile))
}

// toProfileResponse 将应用层资料视图转换为传输层响应。
func toProfileResponse(p *appaccount.Profile) profileResponse {
	return profileResponse{
		UserID:    p.UserID,
		Account:   p.Account,
		Nickname:  p.Nickname,
		AvatarURL: p.AvatarURL,
		Bio:       p.Bio,
	}
}

// mapError 将领域错误映射为 HTTP 状态码：400 参数非法、401 凭证错误、404 不存在、409 冲突。
func mapError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, domainaccount.ErrInvalidAccountName),
		errors.Is(err, domainaccount.ErrInvalidNickname),
		errors.Is(err, domainaccount.ErrEmptyProfileUpdate):
		fail(c, http.StatusBadRequest, err.Error())
	case errors.Is(err, domainaccount.ErrInvalidCredentials):
		fail(c, http.StatusUnauthorized, err.Error())
	case errors.Is(err, domainaccount.ErrAccountNotFound):
		fail(c, http.StatusNotFound, err.Error())
	case errors.Is(err, domainaccount.ErrAccountExists):
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
