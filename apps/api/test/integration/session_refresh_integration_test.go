//go:build integration

// session_refresh_integration_test.go 覆盖文档 11.1 的刷新令牌用例：
// 正常轮换、过期失败、重放全端下线、并发宽限期同一对、登出拒绝名单、容差、锁等待与限流。
package integration

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/redis/go-redis/v9"
	"golang.org/x/crypto/bcrypt"

	appaccount "pulsefeed/internal/application/account"
	domainaccount "pulsefeed/internal/domain/account"
	infracache "pulsefeed/internal/infra/cache"
	infrajwt "pulsefeed/internal/infra/jwt"
	infraaccount "pulsefeed/internal/infra/persistence/account"
	httpaccount "pulsefeed/internal/interfaces/http/account"
	"pulsefeed/internal/interfaces/http/middleware"
)

const (
	// integrationJWTSecret 至少 32 字节，与运行时校验规则一致。
	integrationJWTSecret = "pulsefeed-integration-secret-0123456789"
	integrationPassword  = "secret-123"
)

// sessionServer 是刷新令牌链路的测试装配：真实 MySQL + Redis + Gin 路由。
type sessionServer struct {
	engine   *gin.Engine
	redis    *redis.Client
	issuer   *infrajwt.Issuer
	repo     *infraaccount.Repository
	sessions *infraaccount.SessionRepository
}

// apiEnvelope 是统一响应信封，Data 保留原始 JSON 供各用例按需解码。
type apiEnvelope struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

// sessionData 是登录/刷新响应的 data 字段。
type sessionData struct {
	AccessToken      string `json:"access_token"`
	RefreshToken     string `json:"refresh_token"`
	TokenType        string `json:"token_type"`
	ExpiresIn        int64  `json:"expires_in"`
	RefreshExpiresIn int64  `json:"refresh_expires_in"`
	Reused           *bool  `json:"reused"`
	UserID           int64  `json:"user_id"`
	Account          string `json:"account"`
}

// newSessionServer 装配真实依赖并清空测试 Redis 库，保证限流计数不跨用例串味。
func newSessionServer(t *testing.T) *sessionServer {
	t.Helper()
	gin.SetMode(gin.TestMode)
	client := redis.NewClient(&redis.Options{
		Addr: envOr("PULSEFEED_IT_REDIS_ADDR", defaultRedis),
		DB:   15,
	})
	if err := client.FlushDB(context.Background()).Err(); err != nil {
		t.Fatalf("清空测试 Redis 失败: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	issuer, err := infrajwt.New(integrationJWTSecret, 30*time.Minute, 30*time.Second)
	if err != nil {
		t.Fatalf("初始化签发器失败: %v", err)
	}
	repo := infraaccount.New(testDB)
	sessions := infraaccount.NewSession(testDB)
	denylist := infracache.NewDenylist(client)
	service := appaccount.New(repo,
		appaccount.WithTokenIssuer(issuer),
		appaccount.WithSessionRepository(sessions),
		appaccount.WithRotationCache(infracache.NewRotationCache(client)),
		appaccount.WithRefreshLimiter(infracache.NewRefreshGuard(client)),
		appaccount.WithAccessDenylist(denylist),
		appaccount.WithRefreshPolicy(14*24*time.Hour, 30*time.Second),
	)
	handler := httpaccount.New(service)

	engine := gin.New()
	api := engine.Group("/api")
	api.POST("/sessions", handler.Login)
	api.POST("/sessions/refresh", handler.Refresh)
	authed := api.Group("", middleware.NewJWTAuth(issuer, denylist, nil))
	authed.DELETE("/sessions/current", handler.Logout)
	authed.GET("/users/me", handler.Me)

	return &sessionServer{engine: engine, redis: client, issuer: issuer, repo: repo, sessions: sessions}
}

// registerUser 直接落库一个可登录账号，返回用户 ID。
func (s *sessionServer) registerUser(t *testing.T, account string) int64 {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte(integrationPassword), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("生成密码哈希失败: %v", err)
	}
	acc, err := domainaccount.NewAccount(account, string(hash))
	if err != nil {
		t.Fatalf("构造账号失败: %v", err)
	}
	profile, err := domainaccount.NewUserProfile(0, account)
	if err != nil {
		t.Fatalf("构造资料失败: %v", err)
	}
	if err := s.repo.Create(context.Background(), acc, profile); err != nil {
		t.Fatalf("写入账号失败: %v", err)
	}
	return acc.ID
}

// login 通过真实 HTTP 登录并返回会话。
func (s *sessionServer) login(t *testing.T, account string) sessionData {
	t.Helper()
	rec, env := postJSON(s.engine, "/api/sessions", map[string]any{
		"account":  account,
		"password": integrationPassword,
	}, "")
	assertEnvelope(t, rec, env, http.StatusOK, 0)
	return decodeSession(t, env.Data)
}

// refresh 通过真实 HTTP 刷新并返回原始响应，断言由调用方完成。
func (s *sessionServer) refresh(token string) (*httptest.ResponseRecorder, apiEnvelope) {
	return postJSON(s.engine, "/api/sessions/refresh", map[string]any{"refresh_token": token}, "")
}

// rows 读取指定用户的全部刷新令牌行。
func (s *sessionServer) rows(t *testing.T, userID int64) []infraaccount.RefreshTokenModel {
	t.Helper()
	var rows []infraaccount.RefreshTokenModel
	if err := testDB.Where("user_id = ?", userID).Order("id ASC").Find(&rows).Error; err != nil {
		t.Fatalf("读取刷新令牌行失败: %v", err)
	}
	return rows
}

// findRow 按哈希定位一行，未命中直接失败。
func findRow(t *testing.T, rows []infraaccount.RefreshTokenModel, hash string) infraaccount.RefreshTokenModel {
	t.Helper()
	for _, row := range rows {
		if row.TokenHash == hash {
			return row
		}
	}
	t.Fatalf("未找到 token_hash=%s 的行", hash)
	return infraaccount.RefreshTokenModel{}
}

// replayCount 统计指定用户被判定为重放并吊销的行数。
func (s *sessionServer) replayCount(t *testing.T, userID int64) int64 {
	t.Helper()
	var count int64
	if err := testDB.Model(&infraaccount.RefreshTokenModel{}).
		Where("user_id = ? AND revoked_reason = ?", userID, "replay").Count(&count).Error; err != nil {
		t.Fatalf("统计 replay 行失败: %v", err)
	}
	return count
}

// postJSON 发送 JSON 请求并解析统一信封，可携带 Bearer 令牌。
func postJSON(engine *gin.Engine, path string, body any, token string) (*httptest.ResponseRecorder, apiEnvelope) {
	payload, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)
	return rec, decodeEnvelope(rec)
}

// send 发送任意方法的请求，用于登出与受保护读接口。
func send(engine *gin.Engine, method, path, token string) (*httptest.ResponseRecorder, apiEnvelope) {
	req := httptest.NewRequest(method, path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)
	return rec, decodeEnvelope(rec)
}

// decodeEnvelope 解析响应信封，解析失败返回零值。
func decodeEnvelope(rec *httptest.ResponseRecorder) apiEnvelope {
	var env apiEnvelope
	_ = json.Unmarshal(rec.Body.Bytes(), &env)
	return env
}

// decodeSession 解析会话数据，失败直接终止用例。
func decodeSession(t *testing.T, data json.RawMessage) sessionData {
	t.Helper()
	var session sessionData
	if err := json.Unmarshal(data, &session); err != nil {
		t.Fatalf("解析会话响应失败: %v, data=%s", err, string(data))
	}
	return session
}

// assertEnvelope 断言 HTTP 状态码与业务码。
func assertEnvelope(t *testing.T, rec *httptest.ResponseRecorder, env apiEnvelope, wantStatus, wantCode int) {
	t.Helper()
	if rec.Code != wantStatus {
		t.Fatalf("HTTP 状态码期望 %d，实际 %d，body=%s", wantStatus, rec.Code, rec.Body.String())
	}
	if env.Code != wantCode {
		t.Fatalf("业务码期望 %d，实际 %d，body=%s", wantCode, env.Code, rec.Body.String())
	}
}

// hashToken 与后端保持一致：SHA-256 小写十六进制。
func hashToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// TestRefreshTokenRotation 覆盖 11.1 用例 1：正常刷新轮换并沿用同一 family。
func TestRefreshTokenRotation(t *testing.T) {
	s := newSessionServer(t)
	userID := s.registerUser(t, uniqueKey("refresh-ok"))
	first := s.login(t, uniqueKey("refresh-ok"))
	if first.RefreshToken == "" || first.RefreshExpiresIn != 1209600 || first.ExpiresIn != 1800 {
		t.Fatalf("登录响应字段不符合契约: %+v", first)
	}
	if first.Reused != nil {
		t.Fatalf("登录响应不应包含 reused 字段")
	}

	rec, env := s.refresh(first.RefreshToken)
	assertEnvelope(t, rec, env, http.StatusOK, 0)
	second := decodeSession(t, env.Data)
	if second.Reused == nil || *second.Reused {
		t.Fatalf("正常刷新 reused 期望 false，实际 %+v", second.Reused)
	}
	if second.AccessToken == first.AccessToken || second.RefreshToken == first.RefreshToken {
		t.Fatalf("刷新后必须同时更换 access 与 refresh token")
	}
	if second.ExpiresIn != 1800 || second.RefreshExpiresIn != 1209600 {
		t.Fatalf("刷新响应有效期不符合契约: %+v", second)
	}

	rows := s.rows(t, userID)
	oldRow := findRow(t, rows, hashToken(first.RefreshToken))
	if oldRow.RevokedReason != "rotated" || oldRow.RevokedAt == nil {
		t.Fatalf("旧行应标记 rotated，实际 %+v", oldRow)
	}
	newRow := findRow(t, rows, hashToken(second.RefreshToken))
	if newRow.FamilyID != oldRow.FamilyID {
		t.Fatalf("轮换必须沿用同一 family：旧 %s 新 %s", oldRow.FamilyID, newRow.FamilyID)
	}
	if newRow.RotatedFrom != oldRow.ID {
		t.Fatalf("rotated_from 期望 %d，实际 %d", oldRow.ID, newRow.RotatedFrom)
	}
	if delta := time.Until(newRow.ExpiresAt.Add(-14 * 24 * time.Hour)); delta > 2*time.Minute || delta < -2*time.Minute {
		t.Fatalf("新行 expires_at 应约为 now+14d，偏差 %v", delta)
	}
}

// TestRefreshExpiredToken 覆盖 11.1 用例 2：过期 refresh 失败且不误杀其他链。
func TestRefreshExpiredToken(t *testing.T) {
	s := newSessionServer(t)
	account := uniqueKey("refresh-expired")
	userID := s.registerUser(t, account)
	expiredSession := s.login(t, account)
	otherSession := s.login(t, account)

	if err := testDB.Exec(
		"UPDATE pf_refresh_tokens SET expires_at = NOW(3) - INTERVAL 1 SECOND WHERE token_hash = ?",
		hashToken(expiredSession.RefreshToken),
	).Error; err != nil {
		t.Fatalf("修改过期时间失败: %v", err)
	}

	rec, env := s.refresh(expiredSession.RefreshToken)
	assertEnvelope(t, rec, env, http.StatusUnauthorized, 40103)
	if count := s.replayCount(t, userID); count != 0 {
		t.Fatalf("过期不应判定重放，实际 replay 行数 %d", count)
	}

	// 其他 family 不受影响，仍可正常刷新。
	rec, env = s.refresh(otherSession.RefreshToken)
	assertEnvelope(t, rec, env, http.StatusOK, 0)
}

// TestRefreshReplayRevokesAllFamilies 覆盖 11.1 用例 3：重放触发全端下线。
func TestRefreshReplayRevokesAllFamilies(t *testing.T) {
	s := newSessionServer(t)
	account := uniqueKey("refresh-replay")
	userID := s.registerUser(t, account)
	first := s.login(t, account)
	second := s.login(t, account)

	if err := testDB.Exec(
		"UPDATE pf_refresh_tokens SET revoked_at = NOW(3) - INTERVAL 2 MINUTE, revoked_reason = 'rotated' WHERE token_hash = ?",
		hashToken(first.RefreshToken),
	).Error; err != nil {
		t.Fatalf("构造已轮换旧 token 失败: %v", err)
	}

	rec, env := s.refresh(first.RefreshToken)
	assertEnvelope(t, rec, env, http.StatusUnauthorized, 40103)

	rows := s.rows(t, userID)
	active := 0
	replayedHash := hashToken(first.RefreshToken)
	for _, row := range rows {
		if row.RevokedAt == nil {
			active++
			continue
		}
		// 构造用的那条保留 rotated；其余原本有效的链必须被重放处置为 replay。
		wantReason := "replay"
		if row.TokenHash == replayedHash {
			wantReason = "rotated"
		}
		if row.RevokedReason != wantReason {
			t.Fatalf("重放后行 %d 的 revoked_reason 期望 %s，实际 %s", row.ID, wantReason, row.RevokedReason)
		}
	}
	if active != 0 {
		t.Fatalf("重放后不应存在未吊销行，实际 %d", active)
	}

	rec, env = s.refresh(second.RefreshToken)
	assertEnvelope(t, rec, env, http.StatusUnauthorized, 40103)
}

// TestRefreshConcurrentGrace 覆盖 11.1 用例 4：并发重复提交回放同一对令牌。
func TestRefreshConcurrentGrace(t *testing.T) {
	s := newSessionServer(t)
	account := uniqueKey("refresh-concurrent")
	userID := s.registerUser(t, account)
	first := s.login(t, account)

	type result struct {
		rec *httptest.ResponseRecorder
		env apiEnvelope
	}
	results := make([]result, 2)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			<-start
			rec, env := s.refresh(first.RefreshToken)
			results[index] = result{rec: rec, env: env}
		}(i)
	}
	close(start)
	wg.Wait()

	reused := map[bool]bool{}
	var accessTokens, refreshTokens []string
	for _, item := range results {
		assertEnvelope(t, item.rec, item.env, http.StatusOK, 0)
		session := decodeSession(t, item.env.Data)
		if session.Reused == nil {
			t.Fatalf("刷新响应必须携带 reused 字段")
		}
		reused[*session.Reused] = true
		accessTokens = append(accessTokens, session.AccessToken)
		refreshTokens = append(refreshTokens, session.RefreshToken)
	}
	if !reused[false] || !reused[true] {
		t.Fatalf("reused 集合期望 {false,true}，实际 %v", reused)
	}
	for i := 1; i < len(results); i++ {
		if accessTokens[i] != accessTokens[0] || refreshTokens[i] != refreshTokens[0] {
			t.Fatalf("宽限期必须返回完全相同的一对令牌")
		}
	}
	if rows := s.rows(t, userID); len(rows) != 2 {
		t.Fatalf("并发刷新只应新增一行，实际总行数 %d", len(rows))
	}
}

// TestLogoutDeniesAccessImmediately 覆盖 11.1 用例 5：登出后旧 access 立即 401。
func TestLogoutDeniesAccessImmediately(t *testing.T) {
	s := newSessionServer(t)
	account := uniqueKey("refresh-logout")
	userID := s.registerUser(t, account)
	session := s.login(t, account)

	rec, env := send(s.engine, http.MethodDelete, "/api/sessions/current", session.AccessToken)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("登出期望 204，实际 %d，body=%s", rec.Code, rec.Body.String())
	}
	if env.Code != 0 {
		t.Fatalf("204 不应带错误信封")
	}

	rec, env = send(s.engine, http.MethodGet, "/api/users/me", session.AccessToken)
	assertEnvelope(t, rec, env, http.StatusUnauthorized, 40102)

	claims, err := s.issuer.Parse(session.AccessToken)
	if err != nil {
		t.Fatalf("解析已登出令牌失败: %v", err)
	}
	ttl, err := s.redis.TTL(context.Background(), "pf:v1:auth:denylist:"+claims.JTI).Result()
	if err != nil || ttl <= 0 {
		t.Fatalf("拒绝名单 TTL 应为正，实际 %v err=%v", ttl, err)
	}

	for _, row := range s.rows(t, userID) {
		if row.RevokedReason != "logout" || row.RevokedAt == nil {
			t.Fatalf("登出后当前链应标记 logout，实际 %+v", row)
		}
	}
}

// TestAccessTokenLeeway 覆盖 11.1 用例 6：30 秒容差生效，超容差返回 40101。
func TestAccessTokenLeeway(t *testing.T) {
	s := newSessionServer(t)
	account := uniqueKey("refresh-leeway")
	userID := s.registerUser(t, account)

	withinLeeway := manualAccessToken(t, userID, "family-leeway", 10*time.Second)
	rec, env := send(s.engine, http.MethodGet, "/api/users/me", withinLeeway)
	assertEnvelope(t, rec, env, http.StatusOK, 0)

	beyondLeeway := manualAccessToken(t, userID, "family-leeway", 60*time.Second)
	rec, env = send(s.engine, http.MethodGet, "/api/users/me", beyondLeeway)
	assertEnvelope(t, rec, env, http.StatusUnauthorized, 40101)
}

// TestRefreshLockWaitDoesNotKillFamily 覆盖 11.1 用例 7：锁等待立即返回且不误杀。
func TestRefreshLockWaitDoesNotKillFamily(t *testing.T) {
	s := newSessionServer(t)
	account := uniqueKey("refresh-lock")
	userID := s.registerUser(t, account)
	locked := s.login(t, account)
	other := s.login(t, account)

	tx := testDB.Begin()
	if tx.Error != nil {
		t.Fatalf("开启持锁事务失败: %v", tx.Error)
	}
	var lockedID int64
	if err := tx.Raw("SELECT id FROM pf_refresh_tokens WHERE token_hash = ? FOR UPDATE", hashToken(locked.RefreshToken)).
		Scan(&lockedID).Error; err != nil {
		_ = tx.Rollback()
		t.Fatalf("持有行锁失败: %v", err)
	}
	if lockedID == 0 {
		_ = tx.Rollback()
		t.Fatalf("未命中待锁行")
	}

	started := time.Now()
	rec, env := s.refresh(locked.RefreshToken)
	elapsed := time.Since(started)
	_ = tx.Rollback()

	if elapsed > time.Second {
		t.Fatalf("NOWAIT 应立即返回，实际耗时 %v", elapsed)
	}
	assertEnvelope(t, rec, env, http.StatusUnauthorized, 40103)
	if count := s.replayCount(t, userID); count != 0 {
		t.Fatalf("锁等待不应误判重放，实际 replay 行数 %d", count)
	}

	// 其他 family 仍然有效。
	rec, env = s.refresh(other.RefreshToken)
	assertEnvelope(t, rec, env, http.StatusOK, 0)
}

// TestRefreshRateLimit 覆盖 11.1 用例 8：单 token 60 秒 20 次的限流与 TTL。
func TestRefreshRateLimit(t *testing.T) {
	s := newSessionServer(t)
	account := uniqueKey("refresh-limit")
	userID := s.registerUser(t, account)
	session := s.login(t, account)

	for i := 1; i <= 21; i++ {
		rec, env := s.refresh(session.RefreshToken)
		if i <= 20 {
			assertEnvelope(t, rec, env, http.StatusOK, 0)
			continue
		}
		assertEnvelope(t, rec, env, http.StatusTooManyRequests, http.StatusTooManyRequests)
	}

	if count := s.replayCount(t, userID); count != 0 {
		t.Fatalf("限流不应吊销任何 family，实际 replay 行数 %d", count)
	}
	keys, err := s.redis.Keys(context.Background(), "pf:v1:auth:refresh_tok:*").Result()
	if err != nil || len(keys) == 0 {
		t.Fatalf("限流计数键缺失: %v", err)
	}
	for _, key := range keys {
		ttl, err := s.redis.TTL(context.Background(), key).Result()
		if err != nil || ttl <= 0 {
			t.Fatalf("限流键 %s TTL 应为正，实际 %v err=%v", key, ttl, err)
		}
	}
}

// manualAccessToken 用与运行时相同的密钥构造指定过期时长的 access token。
func manualAccessToken(t *testing.T, userID int64, familyID string, expiredSince time.Duration) string {
	t.Helper()
	issuedAt := time.Now().Add(-expiredSince)
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub":  strconv.FormatInt(userID, 10),
		"role": domainaccount.RoleUser,
		"jti":  fmt.Sprintf("manual-%d", time.Now().UnixNano()),
		"fid":  familyID,
		"iat":  issuedAt.Unix(),
		"exp":  issuedAt.Unix(),
	})
	signed, err := token.SignedString([]byte(integrationJWTSecret))
	if err != nil {
		t.Fatalf("签发手工令牌失败: %v", err)
	}
	return signed
}
