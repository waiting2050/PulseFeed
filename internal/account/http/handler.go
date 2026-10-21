package http

import (
	"errors"
	"log/slog"
	"strconv"

	"github.com/gin-gonic/gin"

	"pulsefeed/internal/account/app"
	"pulsefeed/internal/account/domain"
	"pulsefeed/internal/shared/httpx"
	"pulsefeed/internal/shared/middleware"
)

type Handler struct {
	service *app.Service
	logger  *slog.Logger
}

func NewHandler(service *app.Service, logger *slog.Logger) *Handler {
	return &Handler{service: service, logger: logger}
}

func (h *Handler) RegisterRoutes(api gin.IRouter, auth gin.HandlerFunc) {
	api.POST("/users", h.Register)
	api.POST("/sessions", h.Login)
	api.GET("/profiles/:userId", h.PublicProfile)

	protected := api.Group("")
	protected.Use(auth)
	protected.GET("/users/me", h.Me)
	protected.PATCH("/users/me", h.UpdateMe)
	protected.DELETE("/sessions/current", h.Logout)
}

func (h *Handler) Register(c *gin.Context) {
	var req registerRequest
	if !httpx.BindJSON(c, &req) {
		return
	}

	user, err := h.service.Register(c.Request.Context(), app.RegisterInput{
		Account:  req.Account,
		Password: req.Password,
		Nickname: req.Nickname,
	})
	if err != nil {
		h.respondError(c, err)
		return
	}
	httpx.Created(c, toUserResponse(user))
}

func (h *Handler) Login(c *gin.Context) {
	var req loginRequest
	if !httpx.BindJSON(c, &req) {
		return
	}

	result, err := h.service.Login(c.Request.Context(), req.Account, req.Password)
	if err != nil {
		h.respondError(c, err)
		return
	}
	httpx.OK(c, loginResponse{
		AccessToken: result.Token,
		TokenType:   "Bearer",
		ExpiresIn:   result.ExpiresIn,
		User:        *toUserResponse(result.User),
	})
}

func (h *Handler) Me(c *gin.Context) {
	userID, ok := middleware.CurrentUserID(c)
	if !ok {
		httpx.Unauthorized(c, "未登录")
		return
	}

	user, err := h.service.GetByID(c.Request.Context(), userID)
	if err != nil {
		h.respondError(c, err)
		return
	}
	httpx.OK(c, toUserResponse(user))
}

func (h *Handler) UpdateMe(c *gin.Context) {
	userID, ok := middleware.CurrentUserID(c)
	if !ok {
		httpx.Unauthorized(c, "未登录")
		return
	}

	var req updateProfileRequest
	if !httpx.BindJSON(c, &req) {
		return
	}

	user, err := h.service.UpdateProfile(c.Request.Context(), userID, app.ProfileUpdate{
		Nickname:  req.Nickname,
		AvatarURL: req.AvatarURL,
		Bio:       req.Bio,
	})
	if err != nil {
		h.respondError(c, err)
		return
	}
	httpx.OK(c, toUserResponse(user))
}

func (h *Handler) PublicProfile(c *gin.Context) {
	userID, err := strconv.ParseInt(c.Param("userId"), 10, 64)
	if err != nil || userID <= 0 {
		httpx.BadRequest(c, "用户 ID 不合法")
		return
	}

	user, err := h.service.GetByID(c.Request.Context(), userID)
	if err != nil {
		h.respondError(c, err)
		return
	}
	httpx.OK(c, toUserResponse(user))
}

func (h *Handler) Logout(c *gin.Context) {
	httpx.NoContent(c)
}

func (h *Handler) respondError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, domain.ErrAccountExists):
		httpx.Conflict(c, err.Error())
	case errors.Is(err, domain.ErrInvalidCredentials):
		httpx.Unauthorized(c, err.Error())
	case errors.Is(err, domain.ErrUserNotFound):
		httpx.NotFound(c, err.Error())
	case errors.Is(err, domain.ErrInvalidAccount),
		errors.Is(err, domain.ErrInvalidPassword),
		errors.Is(err, domain.ErrInvalidNickname),
		errors.Is(err, domain.ErrInvalidProfile):
		httpx.BadRequest(c, err.Error())
	default:
		h.logger.Error("account request failed", "error", err)
		httpx.Internal(c, "服务内部错误")
	}
}

func toUserResponse(user *domain.User) *userResponse {
	return &userResponse{
		ID:        user.ID,
		Account:   user.Account,
		Nickname:  user.Nickname,
		AvatarURL: user.AvatarURL,
		Bio:       user.Bio,
		Role:      user.Role,
		Status:    user.Status,
		CreatedAt: user.CreatedAt,
	}
}
