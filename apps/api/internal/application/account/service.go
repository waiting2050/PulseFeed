// service.go 编排账号注册、登录、刷新、资料读写与登出用例，只依赖领域端口与令牌签发端口。
package appaccount

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"time"

	domainaccount "pulsefeed/internal/domain/account"

	"golang.org/x/crypto/bcrypt"
)

// bcrypt 哈希输入上限为 72 字节，超长密码直接判定为非法凭证。
const maxPasswordBytes = 72

// 轮换事务的上下文超时：保证 revoked_at 与提交时间的偏差远小于宽限窗口。
const rotateTimeout = 5 * time.Second

// 宽限期缓存未命中时的重试次数与间隔，覆盖事务提交到缓存写入之间的空窗。
const (
	graceRetries       = 3
	graceRetryInterval = 50 * time.Millisecond
)

// TokenIssuer 是应用层的令牌签发端口，由 infra/jwt 实现并经组合根注入。
type TokenIssuer interface {
	// Issue 为指定用户与会话链签发访问令牌。
	Issue(userID int64, role, familyID string) (string, error)
	// AccessTTL 返回访问令牌有效期，用于计算 expires_in。
	AccessTTL() time.Duration
}

// AuthMetrics 是认证链路的指标端口，未注入时所有调用为空操作。
type AuthMetrics interface {
	ObserveRefresh(result string)
	ObserveRevocations(count int64)
	ObserveDenylistError(result string)
	ObserveLoginLimiterError(scope string)
}

// Profile 是面向传输层的资料视图。
type Profile struct {
	UserID    int64
	Account   string
	Nickname  string
	AvatarURL string
	Bio       string
}

// Session 是登录或刷新成功后的会话视图，携带一对令牌。
type Session struct {
	Profile
	AccessToken      string
	RefreshToken     string
	ExpiresIn        int64
	RefreshExpiresIn int64
	Reused           bool
}

// Option 用于装配可选依赖。
type Option func(*Service)

// WithTokenIssuer 注入令牌签发器，登录与刷新必需。
func WithTokenIssuer(issuer TokenIssuer) Option {
	return func(s *Service) {
		s.issuer = issuer
	}
}

// WithSessionRepository 注入刷新令牌仓储，登录与刷新必需。
func WithSessionRepository(repo domainaccount.SessionRepository) Option {
	return func(s *Service) {
		s.sessions = repo
	}
}

// WithRotationCache 注入宽限期轮换缓存；未注入时并发重复提交直接失败。
func WithRotationCache(cache domainaccount.RotationCache) Option {
	return func(s *Service) {
		s.rotation = cache
	}
}

// WithRefreshLimiter 注入刷新限流器；未注入时不做限流。
func WithRefreshLimiter(limiter domainaccount.RefreshLimiter) Option {
	return func(s *Service) {
		s.limiter = limiter
	}
}

// WithLoginLimiter 注入登录限流器（IP + 账号双层）；未注入时不做限流。
func WithLoginLimiter(limiter domainaccount.LoginLimiter) Option {
	return func(s *Service) {
		s.loginLimiter = limiter
	}
}

// WithAccessDenylist 注入登出拒绝名单；未注入时登出只吊销刷新令牌链。
func WithAccessDenylist(denylist domainaccount.AccessDenylist) Option {
	return func(s *Service) {
		s.denylist = denylist
	}
}

// WithMetrics 注入认证指标。
func WithMetrics(metrics AuthMetrics) Option {
	return func(s *Service) {
		s.metrics = metrics
	}
}

// WithRefreshPolicy 注入刷新令牌有效期与宽限窗口。
func WithRefreshPolicy(ttl, grace time.Duration) Option {
	return func(s *Service) {
		s.refreshTTL = ttl
		s.grace = grace
	}
}

// WithAccessPolicy 注入 access token 的中间件时钟容差；
// 登出拒绝名单的 TTL 必须覆盖该窗口，否则容差内的令牌会在键过期后复活。
func WithAccessPolicy(leeway time.Duration) Option {
	return func(s *Service) {
		s.leeway = leeway
	}
}

// Service 提供账号相关用例。
type Service struct {
	repo   domainaccount.Repository
	issuer TokenIssuer

	sessions     domainaccount.SessionRepository
	rotation     domainaccount.RotationCache
	limiter      domainaccount.RefreshLimiter
	loginLimiter domainaccount.LoginLimiter
	denylist     domainaccount.AccessDenylist
	metrics      AuthMetrics

	refreshTTL time.Duration
	grace      time.Duration
	leeway     time.Duration
}

// New 创建账号应用服务。
func New(repo domainaccount.Repository, opts ...Option) *Service {
	s := &Service{repo: repo}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// Register 注册新账号：bcrypt 哈希密码，事务写入凭证与资料；昵称为空时回退为账号名。
func (s *Service) Register(ctx context.Context, account, password, nickname string) (*Profile, error) {
	if password == "" || len(password) > maxPasswordBytes {
		return nil, domainaccount.ErrInvalidCredentials
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return nil, err
	}

	acc, err := domainaccount.NewAccount(account, string(hash))
	if err != nil {
		return nil, err
	}
	if nickname == "" {
		nickname = acc.Name
	}
	profile, err := domainaccount.NewUserProfile(0, nickname)
	if err != nil {
		return nil, err
	}

	if err := s.repo.Create(ctx, acc, profile); err != nil {
		return nil, err
	}
	return &Profile{
		UserID:    acc.ID,
		Account:   acc.Name,
		Nickname:  profile.Nickname,
		AvatarURL: profile.AvatarURL,
		Bio:       profile.Bio,
	}, nil
}

// Login 校验账号密码，签发访问令牌并为本次登录开一条新会话链。
// 限流在查库与密码校验之前完成，避免爆破流量消耗数据库与 bcrypt 资源。
func (s *Service) Login(ctx context.Context, account, password, deviceLabel, clientIP string) (*Session, error) {
	if s.issuer == nil {
		return nil, errors.New("token issuer 未注入")
	}
	if s.sessions == nil {
		return nil, errors.New("session repository 未注入")
	}
	if s.loginLimiter != nil {
		if clientIP != "" {
			if ok := s.allowLogin("ip", func() (bool, error) { return s.loginLimiter.AllowIP(ctx, clientIP) }); !ok {
				return nil, domainaccount.ErrLoginLimited
			}
		}
		normalized := domainaccount.NormalizeAccountName(account)
		if ok := s.allowLogin("account", func() (bool, error) {
			return s.loginLimiter.AllowAccount(ctx, normalized)
		}); !ok {
			return nil, domainaccount.ErrLoginLimited
		}
	}
	acc, err := s.repo.FindByAccount(ctx, domainaccount.NormalizeAccountName(account))
	if err != nil {
		if errors.Is(err, domainaccount.ErrAccountNotFound) {
			return nil, domainaccount.ErrInvalidCredentials
		}
		return nil, err
	}
	if err := bcrypt.CompareHashAndPassword([]byte(acc.PasswordHash), []byte(password)); err != nil {
		return nil, domainaccount.ErrInvalidCredentials
	}
	// 凭证通过后再校验状态：被禁用或非正常状态的账号不签发令牌，
	// 且不向未通过凭证校验的调用方暴露账号状态。
	if acc.Status != domainaccount.StatusNormal {
		return nil, domainaccount.ErrAccountDisabled
	}

	familyID, err := newFamilyID()
	if err != nil {
		return nil, err
	}
	token, err := s.issuer.Issue(acc.ID, acc.Role, familyID)
	if err != nil {
		return nil, err
	}

	rawRefresh, refreshHash, err := newRefreshToken()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	record := &domainaccount.RefreshToken{
		UserID:      acc.ID,
		FamilyID:    familyID,
		TokenHash:   refreshHash,
		DeviceLabel: deviceLabel,
		IssuedAt:    now,
		ExpiresAt:   now.Add(s.refreshTTL),
	}
	if err := s.sessions.Create(ctx, record); err != nil {
		return nil, err
	}

	return &Session{
		Profile:          Profile{UserID: acc.ID, Account: acc.Name},
		AccessToken:      token,
		RefreshToken:     rawRefresh,
		ExpiresIn:        int64(s.issuer.AccessTTL().Seconds()),
		RefreshExpiresIn: int64(s.refreshTTL.Seconds()),
	}, nil
}

// Refresh 用刷新令牌换新的一对令牌；并发重复提交由宽限期回放同一对结果。
func (s *Service) Refresh(ctx context.Context, raw, clientIP string) (*Session, error) {
	if s.sessions == nil || s.issuer == nil {
		return nil, errors.New("刷新链路依赖未注入")
	}
	if raw == "" {
		return nil, domainaccount.ErrSessionInvalid
	}
	hash := sha256Hex(raw)

	// 先做一次只读查表：第三层限流需要用户 ID，设备标签也从这里沿用。
	existing, err := s.sessions.FindByHash(ctx, hash)
	if err != nil {
		return nil, err
	}

	if s.limiter != nil {
		// 前两层在解析令牌之前完成，避免无效令牌消耗数据库连接。
		if ok := s.allow("token", func() (bool, error) { return s.limiter.AllowToken(ctx, hash) }); !ok {
			return nil, s.limitExceeded()
		}
		if clientIP != "" {
			if ok := s.allow("ip", func() (bool, error) { return s.limiter.AllowIP(ctx, clientIP) }); !ok {
				return nil, s.limitExceeded()
			}
		}
		// 第三层必须在轮换之前完成，避免轮换成功后再返回 429 造成状态不一致。
		if existing != nil {
			if ok := s.allow("user", func() (bool, error) { return s.limiter.AllowUser(ctx, existing.UserID) }); !ok {
				return nil, s.limitExceeded()
			}
		}
	}

	deviceLabel := ""
	if existing != nil {
		deviceLabel = existing.DeviceLabel
	}

	rawNext, nextHash, err := newRefreshToken()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	next := &domainaccount.RefreshToken{
		TokenHash:   nextHash,
		DeviceLabel: deviceLabel,
		IssuedAt:    now,
		ExpiresAt:   now.Add(s.refreshTTL),
	}

	rotateCtx, cancel := context.WithTimeout(ctx, rotateTimeout)
	res, err := s.sessions.Rotate(rotateCtx, hash, next, s.grace)
	cancel()
	if err != nil {
		s.observe("error")
		return nil, err
	}

	switch res.Outcome {
	case domainaccount.RotateNotFound, domainaccount.RotateExpired:
		return nil, s.invalid()
	case domainaccount.RotateReplay:
		// 提交了一枚早已失效的令牌：按泄露处理，吊销该用户全部会话链。
		revoked, revokeErr := s.sessions.RevokeAll(ctx, res.UserID, "replay")
		if revokeErr != nil {
			s.observe("error")
			return nil, revokeErr
		}
		s.observeRevocations(revoked)
		return nil, s.invalidWith("replay")
	case domainaccount.RotateGrace:
		return s.replayRotation(ctx, hash)
	case domainaccount.RotateOK:
		pair, issueErr := s.issuePair(ctx, res.UserID, res.FamilyID, rawNext)
		if issueErr != nil {
			return nil, issueErr
		}
		s.saveRotation(ctx, hash, pair, res.FamilyID)
		s.observe("ok")
		return pair, nil
	default:
		s.observe("error")
		return nil, domainaccount.ErrSessionInvalid
	}
}

// issuePair 读取最新账号状态并签发访问令牌，组装返回给客户端的会话视图。
func (s *Service) issuePair(ctx context.Context, userID int64, familyID, rawRefresh string) (*Session, error) {
	acc, err := s.repo.FindByID(ctx, userID)
	if err != nil {
		// 用户不存在或读取失败时吊销当前链，避免继续签发。
		if revoked, revokeErr := s.sessions.RevokeFamily(ctx, userID, familyID, "logout"); revokeErr == nil {
			s.observeRevocations(revoked)
		}
		if errors.Is(err, domainaccount.ErrAccountNotFound) {
			return nil, s.invalid()
		}
		return nil, err
	}
	if acc.Status != domainaccount.StatusNormal {
		if revoked, revokeErr := s.sessions.RevokeFamily(ctx, userID, familyID, "logout"); revokeErr == nil {
			s.observeRevocations(revoked)
		}
		return nil, s.invalid()
	}

	accessToken, err := s.issuer.Issue(acc.ID, acc.Role, familyID)
	if err != nil {
		return nil, err
	}
	return &Session{
		Profile:          Profile{UserID: acc.ID, Account: acc.Name},
		AccessToken:      accessToken,
		RefreshToken:     rawRefresh,
		ExpiresIn:        int64(s.issuer.AccessTTL().Seconds()),
		RefreshExpiresIn: int64(s.refreshTTL.Seconds()),
	}, nil
}

// replayRotation 从宽限缓存回放同一对令牌，并二次校验新 refresh token 仍然有效。
func (s *Service) replayRotation(ctx context.Context, oldHash string) (*Session, error) {
	if s.rotation == nil {
		return nil, s.graceMiss()
	}
	for attempt := 0; attempt < graceRetries; attempt++ {
		if attempt > 0 {
			select {
			case <-time.After(graceRetryInterval):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		record, ok, err := s.rotation.Load(ctx, oldHash)
		if err != nil {
			log.Printf("warning: 读取轮换缓存失败 (attempt=%d): %v", attempt, err)
			continue
		}
		if !ok || record == nil {
			continue
		}
		// 缓存里的新 refresh token 可能已被后续登出或全端下线吊销，必须回查数据库。
		row, err := s.sessions.FindActiveByHash(ctx, sha256Hex(record.RefreshToken))
		if err != nil {
			return nil, err
		}
		if row == nil || row.RevokedAt != nil || !row.ExpiresAt.After(time.Now()) {
			return nil, s.invalid()
		}
		s.observe("reused")
		return &Session{
			Profile:          Profile{UserID: record.UserID, Account: record.Account},
			AccessToken:      record.AccessToken,
			RefreshToken:     record.RefreshToken,
			ExpiresIn:        record.ExpiresIn,
			RefreshExpiresIn: record.RefreshExpiresIn,
			Reused:           true,
		}, nil
	}
	return nil, s.graceMiss()
}

// saveRotation 在事务提交后写入宽限缓存；失败只告警，不影响本次刷新成功。
func (s *Service) saveRotation(ctx context.Context, oldHash string, pair *Session, familyID string) {
	if s.rotation == nil {
		return
	}
	record := domainaccount.RotationRecord{
		AccessToken:      pair.AccessToken,
		RefreshToken:     pair.RefreshToken,
		TokenType:        "Bearer",
		ExpiresIn:        pair.ExpiresIn,
		RefreshExpiresIn: pair.RefreshExpiresIn,
		UserID:           pair.UserID,
		Account:          pair.Account,
		FamilyID:         familyID,
		IssuedAtUnix:     time.Now().Unix(),
	}
	if err := s.rotation.Save(ctx, oldHash, record, s.grace); err != nil {
		log.Printf("warning: 写入轮换缓存失败 (user_id=%d): %v", pair.UserID, err)
	}
}

// Logout 吊销当前会话链，并把该链的全部 access token 加入拒绝名单。
func (s *Service) Logout(ctx context.Context, userID int64, familyID, jti string, accessExp time.Time) error {
	if s.sessions == nil {
		return errors.New("session repository 未注入")
	}
	// 先写拒绝名单：即使后续数据库吊销失败，当前链的 access token 也已立即失效。
	// jti 键的 TTL 取剩余有效期加容差；宽限期内已过期令牌 ttl<=0，由缓存层钳制为最小值而不是跳过。
	if s.denylist != nil {
		if jti != "" {
			if err := s.denylist.AddToken(ctx, jti, time.Until(accessExp)+s.leeway); err != nil {
				log.Printf("warning: 写入 jti 拒绝名单失败 (user_id=%d): %v", userID, err)
				s.observeDenylistError("write_error")
			}
		}
		// family 键覆盖该链此前轮换出的所有 access token，链内令牌最长剩余有效期即 access TTL。
		if familyID != "" {
			if err := s.denylist.AddFamily(ctx, familyID, s.familyDenyTTL()); err != nil {
				log.Printf("warning: 写入 family 拒绝名单失败 (user_id=%d family=%s): %v", userID, familyID, err)
				s.observeDenylistError("write_error")
			}
		}
	}
	if familyID != "" {
		revoked, err := s.sessions.RevokeFamily(ctx, userID, familyID, "logout")
		if err != nil {
			return err
		}
		s.observeRevocations(revoked)
	}
	return nil
}

// familyDenyTTL 返回会话链拒绝键的存活时间：access 有效期加中间件容差，
// 确保链内最早签发（也最早过期）的令牌在容差窗口内始终被拦截。
func (s *Service) familyDenyTTL() time.Duration {
	ttl := s.leeway
	if s.issuer != nil {
		ttl += s.issuer.AccessTTL()
	}
	return ttl
}

// Me 读取指定用户的展示资料。
func (s *Service) Me(ctx context.Context, userID int64) (*Profile, error) {
	profile, err := s.repo.GetProfile(ctx, userID)
	if err != nil {
		return nil, err
	}
	return &Profile{
		UserID:    profile.UserID,
		Nickname:  profile.Nickname,
		AvatarURL: profile.AvatarURL,
		Bio:       profile.Bio,
	}, nil
}

// UpdateMe 部分更新当前用户资料，nil 字段保持原值。
func (s *Service) UpdateMe(ctx context.Context, userID int64, nickname, avatarURL, bio *string) (*Profile, error) {
	profile, err := s.repo.GetProfile(ctx, userID)
	if err != nil {
		return nil, err
	}
	if err := profile.Update(nickname, avatarURL, bio); err != nil {
		return nil, err
	}
	if err := s.repo.UpdateProfile(ctx, profile); err != nil {
		return nil, err
	}
	return &Profile{
		UserID:    profile.UserID,
		Nickname:  profile.Nickname,
		AvatarURL: profile.AvatarURL,
		Bio:       profile.Bio,
	}, nil
}

// allow 执行一层限流；限流器报错时 fail-open 并打 warning。
func (s *Service) allow(scope string, check func() (bool, error)) bool {
	allowed, err := check()
	if err != nil {
		log.Printf("warning: 刷新限流 Redis 不可用，按放行处理 (scope=%s): %v", scope, err)
		return true
	}
	return allowed
}

// allowLogin 执行一层登录限流；限流器报错时 fail-open，并记录告警与指标便于发现 Redis 故障。
func (s *Service) allowLogin(scope string, check func() (bool, error)) bool {
	allowed, err := check()
	if err != nil {
		log.Printf("warning: 登录限流 Redis 不可用，按放行处理 (scope=%s): %v", scope, err)
		s.observeLoginLimiterError(scope)
		return true
	}
	return allowed
}

// observeLoginLimiterError 记录一次登录限流降级。
func (s *Service) observeLoginLimiterError(scope string) {
	if s.metrics != nil {
		s.metrics.ObserveLoginLimiterError(scope)
	}
}

// observeDenylistError 记录一次拒绝名单读写失败。
func (s *Service) observeDenylistError(result string) {
	if s.metrics != nil {
		s.metrics.ObserveDenylistError(result)
	}
}

// invalid 记录失效并返回统一会话错误。
func (s *Service) invalid() error {
	return s.invalidWith("invalid")
}

// limitExceeded 记录限流并返回可退避重试的错误。
func (s *Service) limitExceeded() error {
	s.observe("limited")
	return domainaccount.ErrRefreshLimited
}

// invalidWith 按原因记录指标并返回统一会话错误。
func (s *Service) invalidWith(result string) error {
	s.observe(result)
	return domainaccount.ErrSessionInvalid
}

// graceMiss 记录宽限期未命中并返回统一会话错误，不吊销任何链。
func (s *Service) graceMiss() error {
	return s.invalidWith("grace_miss")
}

// observe 记录一次刷新结果。
func (s *Service) observe(result string) {
	if s.metrics != nil {
		s.metrics.ObserveRefresh(result)
	}
}

// observeRevocations 累加被吊销的行数。
func (s *Service) observeRevocations(count int64) {
	if s.metrics != nil {
		s.metrics.ObserveRevocations(count)
	}
}

// newRefreshToken 生成 32 字节随机刷新令牌，返回明文与 SHA-256 十六进制哈希。
func newRefreshToken() (string, string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", "", err
	}
	raw := base64.RawURLEncoding.EncodeToString(buf)
	return raw, sha256Hex(raw), nil
}

// sha256Hex 返回输入字符串的 SHA-256 小写十六进制。
func sha256Hex(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

// newFamilyID 生成 UUIDv4 文本形式的会话链 ID，避免引入额外依赖。
func newFamilyID() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	buf[6] = (buf[6] & 0x0f) | 0x40
	buf[8] = (buf[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", buf[0:4], buf[4:6], buf[6:8], buf[8:10], buf[10:16]), nil
}
