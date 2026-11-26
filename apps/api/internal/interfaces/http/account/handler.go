// handler.go 实现账号认证相关 HTTP 端点：参数校验、调用应用服务、领域错误到状态码的映射。
package httpaccount

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	appaccount "pulsefeed/internal/application/account"
	domainaccount "pulsefeed/internal/domain/account"
	"pulsefeed/internal/interfaces/http/middleware"

	"github.com/gin-gonic/gin"
)

const (
	// codeAccessExpired 表示 access token 过期，前端触发一次刷新。
	codeAccessExpired = 40101
	// codeAccessInvalid 表示 access token 无效或命中拒绝名单。
	codeAccessInvalid = 40102
	// codeSessionInvalid 表示 refresh token 失效，需要重新登录。
	codeSessionInvalid = 40103
	// loginRetryAfterSeconds 是登录限流触发后建议的退避秒数，与限流窗口一致。
	loginRetryAfterSeconds = "60"
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

// Login 处理 POST /api/sessions，成功返回 200 与双令牌会话。
func (h *Handler) Login(c *gin.Context) {
	var req loginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "请求体非法")
		return
	}
	session, err := h.service.Login(c.Request.Context(), req.Account, req.Password, deviceLabel(c), c.ClientIP())
	if err != nil {
		mapError(c, err)
		return
	}
	ok(c, http.StatusOK, toSessionResponse(session, false))
}

// Refresh 处理 POST /api/sessions/refresh：用 refresh token 换新的一对令牌。
func (h *Handler) Refresh(c *gin.Context) {
	var req refreshRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "请求体非法")
		return
	}
	if req.RefreshToken == "" {
		fail(c, http.StatusBadRequest, "refresh_token 不能为空")
		return
	}
	session, err := h.service.Refresh(c.Request.Context(), req.RefreshToken, c.ClientIP())
	if err != nil {
		mapSessionError(c, err)
		return
	}
	ok(c, http.StatusOK, toSessionResponse(session, true))
}

// Logout 处理 DELETE /api/sessions/current：吊销当前会话链并把当前 access token 的 jti 写入拒绝名单。
func (h *Handler) Logout(c *gin.Context) {
	userID, authed := middleware.UserIDFromContext(c)
	if !authed {
		failCode(c, http.StatusUnauthorized, codeAccessInvalid, "missing or invalid token")
		return
	}
	info, _ := middleware.AccessInfoFrom(c)
	if err := h.service.Logout(c.Request.Context(), userID, info.FamilyID, info.JTI, info.ExpiresAt); err != nil {
		mapError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// Me 处理 GET /api/users/me，返回当前用户资料。
func (h *Handler) Me(c *gin.Context) {
	userID, authed := middleware.UserIDFromContext(c)
	if !authed {
		failCode(c, http.StatusUnauthorized, codeAccessInvalid, "missing or invalid token")
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
		failCode(c, http.StatusUnauthorized, codeAccessInvalid, "missing or invalid token")
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

// toSessionResponse 将会话视图转换为传输层响应；includeReused 仅在刷新接口为 true。
func toSessionResponse(s *appaccount.Session, includeReused bool) sessionResponse {
	resp := sessionResponse{
		AccessToken:      s.AccessToken,
		RefreshToken:     s.RefreshToken,
		TokenType:        "Bearer",
		ExpiresIn:        s.ExpiresIn,
		RefreshExpiresIn: s.RefreshExpiresIn,
		UserID:           s.UserID,
		Account:          s.Account,
	}
	if includeReused {
		reused := s.Reused
		resp.Reused = &reused
	}
	return resp
}

// deviceLabel 取 User-Agent 作为设备标签，截断到 128 个字符；缺失时写 unknown。
func deviceLabel(c *gin.Context) string {
	ua := strings.TrimSpace(c.GetHeader("User-Agent"))
	if ua == "" {
		return "unknown"
	}
	runes := []rune(ua)
	if len(runes) > 128 {
		ua = string(runes[:128])
	}
	return ua
}

// mapError 将领域错误映射为 HTTP 状态码：400 参数非法、401 凭证错误、404 不存在、409 冲突、429 限流。
func mapError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, domainaccount.ErrInvalidAccountName),
		errors.Is(err, domainaccount.ErrInvalidNickname),
		errors.Is(err, domainaccount.ErrEmptyProfileUpdate):
		fail(c, http.StatusBadRequest, err.Error())
	case errors.Is(err, domainaccount.ErrInvalidCredentials):
		fail(c, http.StatusUnauthorized, err.Error())
	case errors.Is(err, domainaccount.ErrAccountDisabled):
		fail(c, http.StatusForbidden, err.Error())
	case errors.Is(err, domainaccount.ErrAccountNotFound):
		fail(c, http.StatusNotFound, err.Error())
	case errors.Is(err, domainaccount.ErrAccountExists):
		fail(c, http.StatusConflict, err.Error())
	case errors.Is(err, domainaccount.ErrLoginLimited):
		// 登录限流按固定 60 秒窗口计数，显式告知客户端退避时长。
		c.Header("Retry-After", loginRetryAfterSeconds)
		fail(c, http.StatusTooManyRequests, err.Error())
	default:
		fail(c, http.StatusInternalServerError, "内部错误")
	}
}

// mapSessionError 将会话接口错误映射为业务码：40103 会话失效、429 限流、其余 500。
func mapSessionError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, domainaccount.ErrSessionInvalid):
		failCode(c, http.StatusUnauthorized, codeSessionInvalid, err.Error())
	case errors.Is(err, domainaccount.ErrRefreshLimited):
		failCode(c, http.StatusTooManyRequests, http.StatusTooManyRequests, err.Error())
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
	failCode(c, status, status, message)
}

// failCode 以统一信封返回错误响应，code 可与 HTTP 状态码不同（会话接口的 401xx）。
func failCode(c *gin.Context, status, code int, message string) {
	c.JSON(status, gin.H{"code": code, "message": message})
}
