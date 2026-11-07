// account_test.go 以内存 fake 仓储覆盖账号注册、登录与个人资料更新的应用层行为。
package appaccount_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	appaccount "pulsefeed/internal/application/account"
	domainaccount "pulsefeed/internal/domain/account"

	"golang.org/x/crypto/bcrypt"
)

// accountFakeRepo 是内存版账号仓储，模拟唯一索引冲突、自增 ID 与资料读写。
type accountFakeRepo struct {
	accounts    map[string]*domainaccount.Account
	profiles    map[int64]*domainaccount.UserProfile
	nextID      int64
	createCalls int
	findCalls   int
	updateCalls int
}

// newAccountFakeRepo 构造空的内存仓储。
func newAccountFakeRepo() *accountFakeRepo {
	return &accountFakeRepo{
		accounts: make(map[string]*domainaccount.Account),
		profiles: make(map[int64]*domainaccount.UserProfile),
	}
}

func (f *accountFakeRepo) Create(_ context.Context, account *domainaccount.Account, profile *domainaccount.UserProfile) error {
	f.createCalls++
	if _, exists := f.accounts[account.Name]; exists {
		return domainaccount.ErrAccountExists
	}
	f.nextID++
	account.ID = f.nextID
	profile.UserID = account.ID
	storedAccount := *account
	storedProfile := *profile
	f.accounts[account.Name] = &storedAccount
	f.profiles[storedProfile.UserID] = &storedProfile
	return nil
}

func (f *accountFakeRepo) FindByAccount(_ context.Context, name string) (*domainaccount.Account, error) {
	f.findCalls++
	account, ok := f.accounts[name]
	if !ok {
		return nil, domainaccount.ErrAccountNotFound
	}
	clone := *account
	return &clone, nil
}

func (f *accountFakeRepo) GetProfile(_ context.Context, userID int64) (*domainaccount.UserProfile, error) {
	profile, ok := f.profiles[userID]
	if !ok {
		return nil, domainaccount.ErrAccountNotFound
	}
	clone := *profile
	return &clone, nil
}

func (f *accountFakeRepo) UpdateProfile(_ context.Context, profile *domainaccount.UserProfile) error {
	f.updateCalls++
	if _, ok := f.profiles[profile.UserID]; !ok {
		return domainaccount.ErrAccountNotFound
	}
	clone := *profile
	f.profiles[profile.UserID] = &clone
	return nil
}

// accountFakeIssuer 记录签发入参并按配置返回令牌或错误。
type accountFakeIssuer struct {
	token      string
	ttl        time.Duration
	issueErr   error
	issuedUser int64
	issuedRole string
}

func (f *accountFakeIssuer) Issue(userID int64, role string) (string, error) {
	f.issuedUser = userID
	f.issuedRole = role
	if f.issueErr != nil {
		return "", f.issueErr
	}
	return f.token, nil
}

func (f *accountFakeIssuer) AccessTTL() time.Duration {
	return f.ttl
}

// newAccountService 装配内存仓储与固定令牌签发器。
func newAccountService() (*appaccount.Service, *accountFakeRepo, *accountFakeIssuer) {
	repo := newAccountFakeRepo()
	issuer := &accountFakeIssuer{token: "token-abc", ttl: 15 * time.Minute}
	return appaccount.New(repo, appaccount.WithTokenIssuer(issuer)), repo, issuer
}

// TestAccountRegisterCreatesProfile 验证注册成功时的规范化、昵称回退与哈希落库。
func TestAccountRegisterCreatesProfile(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name       string
		account    string
		nickname   string
		wantName   string
		wantNick   string
		wantUserID int64
	}{
		{name: "昵称留空回退为账号名", account: "  Alice01 ", wantName: "alice01", wantNick: "alice01", wantUserID: 1},
		{name: "显式昵称去空白", account: "bob_2026", nickname: "  小播  ", wantName: "bob_2026", wantNick: "小播", wantUserID: 2},
	}

	svc, repo, _ := newAccountService()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			profile, err := svc.Register(ctx, tt.account, "secret-123", tt.nickname)
			if err != nil {
				t.Fatalf("注册不应失败: %v", err)
			}
			if profile.UserID != tt.wantUserID || profile.Account != tt.wantName || profile.Nickname != tt.wantNick {
				t.Fatalf("注册返回不符合预期: %+v", profile)
			}
			if profile.AvatarURL != "" || profile.Bio != "" {
				t.Fatalf("新资料的头像/简介应为空: %+v", profile)
			}

			stored := repo.accounts[tt.wantName]
			if stored == nil {
				t.Fatalf("账号 %q 未落库", tt.wantName)
			}
			if stored.PasswordHash == "" || stored.PasswordHash == "secret-123" {
				t.Fatalf("密码必须经过哈希存储: %q", stored.PasswordHash)
			}
			if err := bcrypt.CompareHashAndPassword([]byte(stored.PasswordHash), []byte("secret-123")); err != nil {
				t.Fatalf("落库哈希无法校验原密码: %v", err)
			}
			if stored.Role != domainaccount.RoleUser || stored.Status != domainaccount.StatusNormal {
				t.Fatalf("默认角色/状态不正确: role=%q status=%d", stored.Role, stored.Status)
			}
			storedProfile := repo.profiles[stored.ID]
			if storedProfile == nil || storedProfile.Nickname != tt.wantNick {
				t.Fatalf("资料未随账号一并写入: %+v", storedProfile)
			}
		})
	}
}

// TestAccountRegisterRejectsDuplicateName 验证大小写/空白规范化后仍命中唯一索引冲突。
func TestAccountRegisterRejectsDuplicateName(t *testing.T) {
	ctx := context.Background()
	svc, repo, _ := newAccountService()

	if _, err := svc.Register(ctx, "alice", "secret-123", ""); err != nil {
		t.Fatalf("首次注册不应失败: %v", err)
	}
	_, err := svc.Register(ctx, "  ALICE  ", "other-secret", "")
	if !errors.Is(err, domainaccount.ErrAccountExists) {
		t.Fatalf("重复账号期望 ErrAccountExists，实际 %v", err)
	}
	if repo.createCalls != 2 {
		t.Fatalf("仓储 Create 调用次数 = %d, 期望 2", repo.createCalls)
	}
	if len(repo.profiles) != 1 {
		t.Fatalf("重复注册不应新增资料, 实际 %d 条", len(repo.profiles))
	}
}

// TestAccountRegisterRejectsInvalidInput 验证密码、账号名与昵称的校验分支均不落库。
func TestAccountRegisterRejectsInvalidInput(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name      string
		account   string
		password  string
		nickname  string
		wantError error
	}{
		{name: "空密码", account: "alice", password: "", wantError: domainaccount.ErrInvalidCredentials},
		{name: "超过 bcrypt 上限的密码", account: "alice", password: strings.Repeat("p", 73), wantError: domainaccount.ErrInvalidCredentials},
		{name: "账号名过短", account: "ab", password: "secret-123", wantError: domainaccount.ErrInvalidAccountName},
		{name: "账号名仅空白", account: "   ", password: "secret-123", wantError: domainaccount.ErrInvalidAccountName},
		{name: "昵称超长", account: "alice", password: "secret-123", nickname: strings.Repeat("n", 65), wantError: domainaccount.ErrInvalidNickname},
		{name: "昵称仅空白", account: "alice", password: "secret-123", nickname: "   ", wantError: domainaccount.ErrInvalidNickname},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, repo, _ := newAccountService()
			if _, err := svc.Register(ctx, tt.account, tt.password, tt.nickname); !errors.Is(err, tt.wantError) {
				t.Fatalf("期望错误 %v，实际 %v", tt.wantError, err)
			}
			if repo.createCalls != 0 {
				t.Fatalf("非法注册不应调用仓储 Create, 实际 %d 次", repo.createCalls)
			}
		})
	}
}

// TestAccountRegisterAcceptsPasswordBoundary 验证 72 字节密码可注册、73 字节被拒。
func TestAccountRegisterAcceptsPasswordBoundary(t *testing.T) {
	ctx := context.Background()
	svc, _, _ := newAccountService()

	if _, err := svc.Register(ctx, "boundary", strings.Repeat("p", 72), ""); err != nil {
		t.Fatalf("72 字节密码应可注册: %v", err)
	}
	if _, err := svc.Register(ctx, "overflow", strings.Repeat("p", 73), ""); !errors.Is(err, domainaccount.ErrInvalidCredentials) {
		t.Fatalf("73 字节密码期望 ErrInvalidCredentials，实际 %v", err)
	}
}

// TestAccountLoginIssuesToken 验证正确凭证登录返回规范化账号、用户 ID 与令牌有效期。
func TestAccountLoginIssuesToken(t *testing.T) {
	ctx := context.Background()
	svc, _, issuer := newAccountService()

	if _, err := svc.Register(ctx, "BobChen", "secret-123", ""); err != nil {
		t.Fatalf("注册不应失败: %v", err)
	}
	session, err := svc.Login(ctx, "  BOBCHEN  ", "secret-123")
	if err != nil {
		t.Fatalf("正确凭证登录不应失败: %v", err)
	}
	if session.UserID != 1 || session.Account != "bobchen" {
		t.Fatalf("登录会话账号信息不正确: %+v", session.Profile)
	}
	if session.AccessToken != "token-abc" {
		t.Fatalf("访问令牌 = %q, 期望 token-abc", session.AccessToken)
	}
	if session.ExpiresIn != 900 {
		t.Fatalf("expires_in = %d, 期望 900", session.ExpiresIn)
	}
	if issuer.issuedUser != 1 || issuer.issuedRole != domainaccount.RoleUser {
		t.Fatalf("签发入参不正确: user=%d role=%q", issuer.issuedUser, issuer.issuedRole)
	}
}

// TestAccountLoginRejectsWrongPassword 验证密码错误时统一返回 ErrInvalidCredentials。
func TestAccountLoginRejectsWrongPassword(t *testing.T) {
	ctx := context.Background()
	svc, _, _ := newAccountService()

	if _, err := svc.Register(ctx, "alice", "secret-123", ""); err != nil {
		t.Fatalf("注册不应失败: %v", err)
	}
	session, err := svc.Login(ctx, "alice", "wrong-secret")
	if !errors.Is(err, domainaccount.ErrInvalidCredentials) {
		t.Fatalf("错误密码期望 ErrInvalidCredentials，实际 %v", err)
	}
	if session != nil {
		t.Fatalf("登录失败不应返回会话: %+v", session)
	}
}

// TestAccountLoginHidesUnknownAccount 验证账号不存在被映射为凭证错误而非暴露存在性。
func TestAccountLoginHidesUnknownAccount(t *testing.T) {
	ctx := context.Background()
	svc, repo, _ := newAccountService()

	_, err := svc.Login(ctx, "nobody", "secret-123")
	if !errors.Is(err, domainaccount.ErrInvalidCredentials) {
		t.Fatalf("不存在账号期望 ErrInvalidCredentials，实际 %v", err)
	}
	if errors.Is(err, domainaccount.ErrAccountNotFound) {
		t.Fatalf("不应向调用方暴露 ErrAccountNotFound")
	}
	if repo.findCalls != 1 {
		t.Fatalf("应查询一次账号, 实际 %d 次", repo.findCalls)
	}
}

// TestAccountLoginRequiresIssuer 验证未注入令牌签发器时登录直接失败。
func TestAccountLoginRequiresIssuer(t *testing.T) {
	ctx := context.Background()
	svc := appaccount.New(newAccountFakeRepo())

	session, err := svc.Login(ctx, "alice", "secret-123")
	if err == nil {
		t.Fatalf("未注入 issuer 时登录应失败")
	}
	if session != nil {
		t.Fatalf("失败时不应返回会话")
	}
	if !strings.Contains(err.Error(), "token issuer") {
		t.Fatalf("错误信息应说明缺少 token issuer, 实际 %v", err)
	}
}

// TestAccountMeReadsProfile 验证资料读取与不存在映射。
func TestAccountMeReadsProfile(t *testing.T) {
	ctx := context.Background()
	repo := newAccountFakeRepo()
	repo.profiles[1] = &domainaccount.UserProfile{
		UserID:    1,
		Nickname:  "小播",
		AvatarURL: "/avatar/1.png",
		Bio:       "个人简介",
	}
	svc := appaccount.New(repo)

	profile, err := svc.Me(ctx, 1)
	if err != nil {
		t.Fatalf("读取资料不应失败: %v", err)
	}
	if profile.UserID != 1 || profile.Nickname != "小播" || profile.AvatarURL != "/avatar/1.png" || profile.Bio != "个人简介" {
		t.Fatalf("资料视图不正确: %+v", profile)
	}
	if profile.Account != "" {
		t.Fatalf("Me 视图不应填充账号名: %+v", profile)
	}

	if _, err := svc.Me(ctx, 99); !errors.Is(err, domainaccount.ErrAccountNotFound) {
		t.Fatalf("不存在用户期望 ErrAccountNotFound，实际 %v", err)
	}
}

// TestAccountUpdateMeAppliesPartialFields 验证空更新/非法昵称被拒，合法部分更新落库。
func TestAccountUpdateMeAppliesPartialFields(t *testing.T) {
	ctx := context.Background()
	repo := newAccountFakeRepo()
	repo.profiles[1] = &domainaccount.UserProfile{UserID: 1, Nickname: "旧昵称"}
	svc := appaccount.New(repo)

	if _, err := svc.UpdateMe(ctx, 1, nil, nil, nil); !errors.Is(err, domainaccount.ErrEmptyProfileUpdate) {
		t.Fatalf("空更新期望 ErrEmptyProfileUpdate，实际 %v", err)
	}
	if repo.updateCalls != 0 {
		t.Fatalf("空更新不应写仓储, 实际 %d 次", repo.updateCalls)
	}

	blank := "   "
	if _, err := svc.UpdateMe(ctx, 1, &blank, nil, nil); !errors.Is(err, domainaccount.ErrInvalidNickname) {
		t.Fatalf("空白昵称期望 ErrInvalidNickname，实际 %v", err)
	}
	if repo.updateCalls != 0 {
		t.Fatalf("非法昵称不应写仓储, 实际 %d 次", repo.updateCalls)
	}

	nickname := "  新昵称  "
	avatar := "/avatar/new.png"
	bio := "  新简介  "
	profile, err := svc.UpdateMe(ctx, 1, &nickname, &avatar, &bio)
	if err != nil {
		t.Fatalf("合法部分更新不应失败: %v", err)
	}
	if profile.Nickname != "新昵称" || profile.AvatarURL != avatar || profile.Bio != "新简介" {
		t.Fatalf("更新返回值不正确: %+v", profile)
	}
	if repo.updateCalls != 1 {
		t.Fatalf("合法更新应写仓储一次, 实际 %d 次", repo.updateCalls)
	}
	stored := repo.profiles[1]
	if stored.Nickname != "新昵称" || stored.AvatarURL != avatar || stored.Bio != "新简介" {
		t.Fatalf("仓储资料未更新: %+v", stored)
	}

	if _, err := svc.UpdateMe(ctx, 99, &nickname, nil, nil); !errors.Is(err, domainaccount.ErrAccountNotFound) {
		t.Fatalf("不存在用户期望 ErrAccountNotFound，实际 %v", err)
	}
}
