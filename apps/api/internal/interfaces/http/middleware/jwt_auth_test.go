// jwt_auth_test.go 覆盖三种鉴权中间件的放行/拦截矩阵与 401 错误信封格式：
// 必选 JWT 缺失或非法必须拦、可选 JWT 无令牌放行但带坏令牌必须拦、
// 内部 token 必须常量比较校验；拦截响应统一为 code=401 的 JSON 信封。
package middleware

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	infrajwt "pulsefeed/internal/infra/jwt"

	"github.com/gin-gonic/gin"
)

// newTestIssuer 创建测试用签发器，令牌有效期足够覆盖用例执行时长。
func newTestIssuer(t *testing.T) *infrajwt.Issuer {
	t.Helper()
	issuer, err := infrajwt.New("test-secret", time.Hour)
	if err != nil {
		t.Fatalf("创建签发器失败: %v", err)
	}
	return issuer
}

// issueTestToken 为指定用户签发合法令牌。
func issueTestToken(t *testing.T, issuer *infrajwt.Issuer, userID int64) string {
	t.Helper()
	token, err := issuer.Issue(userID, "user")
	if err != nil {
		t.Fatalf("签发令牌失败: %v", err)
	}
	return token
}

// serveWithMiddleware 挂载中间件与回显用户 ID 的探针 handler，返回响应记录器。
func serveWithMiddleware(mw gin.HandlerFunc, authorization string) *httptest.ResponseRecorder {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(mw)
	engine.GET("/probe", func(c *gin.Context) {
		userID, ok := UserIDFromContext(c)
		c.JSON(http.StatusOK, gin.H{"user_id": userID, "has_user": ok})
	})

	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)
	return rec
}

// assertUnauthorizedEnvelope 断言 401 响应体是 {code, message} 错误信封。
func assertUnauthorizedEnvelope(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("期望状态码 401，实际 %d，body=%s", rec.Code, rec.Body.String())
	}
	var envelope struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("错误信封不是合法 JSON: %v, body=%s", err, rec.Body.String())
	}
	if envelope.Code != http.StatusUnauthorized {
		t.Fatalf("错误信封 code 期望 401，实际 %d", envelope.Code)
	}
	if envelope.Message == "" {
		t.Fatalf("错误信封 message 不应为空")
	}
}

// TestRequiredJWTAuth 覆盖必选 JWT：缺失、无 Bearer 前缀、签名错误、过期全部拦截，合法令牌写入 user_id。
func TestRequiredJWTAuth(t *testing.T) {
	issuer := newTestIssuer(t)
	validToken := issueTestToken(t, issuer, 42)

	otherIssuer, err := infrajwt.New("another-secret", time.Hour)
	if err != nil {
		t.Fatalf("创建另一签发器失败: %v", err)
	}
	foreignToken := issueTestToken(t, otherIssuer, 42)

	expiredIssuer, err := infrajwt.New("test-secret", time.Millisecond)
	if err != nil {
		t.Fatalf("创建短 TTL 签发器失败: %v", err)
	}
	expiredToken := issueTestToken(t, expiredIssuer, 42)
	time.Sleep(5 * time.Millisecond)

	unauthorizedCases := map[string]string{
		"缺失 Authorization": "",
		"缺少 Bearer 前缀":     validToken,
		"令牌为空":             "Bearer ",
		"签名不匹配":            "Bearer " + foreignToken,
		"令牌已过期":            "Bearer " + expiredToken,
	}
	for name, header := range unauthorizedCases {
		t.Run(name, func(t *testing.T) {
			assertUnauthorizedEnvelope(t, serveWithMiddleware(NewJWTAuth(issuer), header))
		})
	}

	t.Run("合法令牌", func(t *testing.T) {
		rec := serveWithMiddleware(NewJWTAuth(issuer), "Bearer "+validToken)
		if rec.Code != http.StatusOK {
			t.Fatalf("期望 200，实际 %d，body=%s", rec.Code, rec.Body.String())
		}
		var body struct {
			UserID  int64 `json:"user_id"`
			HasUser bool  `json:"has_user"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("解析响应失败: %v", err)
		}
		if !body.HasUser || body.UserID != 42 {
			t.Fatalf("期望写入 user_id=42，实际 has_user=%v user_id=%d", body.HasUser, body.UserID)
		}
	})
}

// TestOptionalJWTAuth 覆盖可选 JWT：无令牌放行且不写用户，带坏令牌仍必须拦截。
func TestOptionalJWTAuth(t *testing.T) {
	issuer := newTestIssuer(t)

	t.Run("无令牌放行", func(t *testing.T) {
		rec := serveWithMiddleware(NewOptionalJWTAuth(issuer), "")
		if rec.Code != http.StatusOK {
			t.Fatalf("期望 200，实际 %d", rec.Code)
		}
		var body struct {
			HasUser bool `json:"has_user"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("解析响应失败: %v", err)
		}
		if body.HasUser {
			t.Fatalf("匿名请求不应写入 user_id")
		}
	})

	t.Run("合法令牌写入用户", func(t *testing.T) {
		token := issueTestToken(t, issuer, 7)
		rec := serveWithMiddleware(NewOptionalJWTAuth(issuer), "Bearer "+token)
		if rec.Code != http.StatusOK {
			t.Fatalf("期望 200，实际 %d", rec.Code)
		}
		var body struct {
			UserID  int64 `json:"user_id"`
			HasUser bool  `json:"has_user"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("解析响应失败: %v", err)
		}
		if !body.HasUser || body.UserID != 7 {
			t.Fatalf("期望 user_id=7，实际 has_user=%v user_id=%d", body.HasUser, body.UserID)
		}
	})

	t.Run("坏令牌拦截", func(t *testing.T) {
		assertUnauthorizedEnvelope(t, serveWithMiddleware(NewOptionalJWTAuth(issuer), "Bearer not-a-jwt"))
	})
}

// TestInternalTokenAuth 覆盖内部 token：缺失、长度相同但内容错误、内容正确三种情况。
func TestInternalTokenAuth(t *testing.T) {
	const expected = "internal-secret"

	t.Run("缺失头拦截", func(t *testing.T) {
		assertUnauthorizedEnvelope(t, serveWithMiddleware(NewInternalTokenAuth(expected), ""))
	})

	t.Run("错误 token 拦截", func(t *testing.T) {
		gin.SetMode(gin.TestMode)
		engine := gin.New()
		engine.Use(NewInternalTokenAuth(expected))
		engine.GET("/probe", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{}) })
		req := httptest.NewRequest(http.MethodGet, "/probe", nil)
		req.Header.Set("X-Internal-Token", "internal-secreX")
		rec := httptest.NewRecorder()
		engine.ServeHTTP(rec, req)
		assertUnauthorizedEnvelope(t, rec)
	})

	t.Run("正确 token 放行", func(t *testing.T) {
		gin.SetMode(gin.TestMode)
		engine := gin.New()
		engine.Use(NewInternalTokenAuth(expected))
		engine.GET("/probe", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"ok": true}) })
		req := httptest.NewRequest(http.MethodGet, "/probe", nil)
		req.Header.Set("X-Internal-Token", expected)
		rec := httptest.NewRecorder()
		engine.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("期望 200，实际 %d，body=%s", rec.Code, rec.Body.String())
		}
	})
}
