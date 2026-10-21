package httpx

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

const (
	CodeOK           = 0
	CodeInvalidInput = 1001
	CodeUnauthorized = 1002
	CodeNotFound     = 1003
	CodeConflict     = 1004
	CodeInternal     = 1005
	CodeForbidden    = 1006
)

type Response struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

func OK(c *gin.Context, data any) {
	c.JSON(http.StatusOK, Response{Code: CodeOK, Message: "ok", Data: data})
}

func Created(c *gin.Context, data any) {
	c.JSON(http.StatusCreated, Response{Code: CodeOK, Message: "ok", Data: data})
}

func NoContent(c *gin.Context) {
	c.Status(http.StatusNoContent)
}

func Fail(c *gin.Context, status, code int, message string) {
	c.AbortWithStatusJSON(status, Response{Code: code, Message: message})
}

func BadRequest(c *gin.Context, message string) {
	Fail(c, http.StatusBadRequest, CodeInvalidInput, message)
}

func Unauthorized(c *gin.Context, message string) {
	Fail(c, http.StatusUnauthorized, CodeUnauthorized, message)
}

func NotFound(c *gin.Context, message string) {
	Fail(c, http.StatusNotFound, CodeNotFound, message)
}

func Conflict(c *gin.Context, message string) {
	Fail(c, http.StatusConflict, CodeConflict, message)
}

func Forbidden(c *gin.Context, message string) {
	Fail(c, http.StatusForbidden, CodeForbidden, message)
}

func Internal(c *gin.Context, message string) {
	Fail(c, http.StatusInternalServerError, CodeInternal, message)
}

func BindJSON(c *gin.Context, dst any) bool {
	if err := c.ShouldBindJSON(dst); err != nil {
		BadRequest(c, "请求参数格式不正确")
		return false
	}
	return true
}
