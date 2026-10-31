// entity_test.go 以表驱动方式覆盖 account 领域的校验分支。
package domainaccount

import (
	"errors"
	"strings"
	"testing"
)

func TestNewAccount(t *testing.T) {
	tests := []struct {
		name      string
		account   string
		wantName  string
		wantError error
	}{
		{name: "合法账号名", account: "alice01", wantName: "alice01"},
		{name: "规范化去空白并转小写", account: "  BobChen ", wantName: "bobchen"},
		{name: "空账号名", account: "", wantError: ErrInvalidAccountName},
		{name: "仅空白字符", account: "   ", wantError: ErrInvalidAccountName},
		{name: "低于最小长度", account: "ab", wantError: ErrInvalidAccountName},
		{name: "等于最小长度", account: "abc", wantName: "abc"},
		{name: "等于最大长度", account: strings.Repeat("a", accountNameMaxLen), wantName: strings.Repeat("a", accountNameMaxLen)},
		{name: "超过最大长度", account: strings.Repeat("a", accountNameMaxLen+1), wantError: ErrInvalidAccountName},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			account, err := NewAccount(tt.account, "hashed")
			if tt.wantError != nil {
				if !errors.Is(err, tt.wantError) {
					t.Fatalf("期望错误 %v，实际 %v", tt.wantError, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("不应返回错误: %v", err)
			}
			if account.Name != tt.wantName {
				t.Fatalf("账号名不符合预期: got %q want %q", account.Name, tt.wantName)
			}
			if account.Role != RoleUser || account.Status != StatusNormal {
				t.Fatalf("默认角色/状态不正确: role=%q status=%d", account.Role, account.Status)
			}
		})
	}
}

func TestNewUserProfile(t *testing.T) {
	tests := []struct {
		name      string
		nickname  string
		wantName  string
		wantError error
	}{
		{name: "合法昵称", nickname: "小播", wantName: "小播"},
		{name: "昵称去空白", nickname: "  alice  ", wantName: "alice"},
		{name: "空昵称", nickname: "", wantError: ErrInvalidNickname},
		{name: "仅空白字符", nickname: "   ", wantError: ErrInvalidNickname},
		{name: "等于最大长度", nickname: strings.Repeat("n", nicknameMaxLen), wantName: strings.Repeat("n", nicknameMaxLen)},
		{name: "超过最大长度", nickname: strings.Repeat("n", nicknameMaxLen+1), wantError: ErrInvalidNickname},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			profile, err := NewUserProfile(1, tt.nickname)
			if tt.wantError != nil {
				if !errors.Is(err, tt.wantError) {
					t.Fatalf("期望错误 %v，实际 %v", tt.wantError, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("不应返回错误: %v", err)
			}
			if profile.Nickname != tt.wantName || profile.UserID != 1 {
				t.Fatalf("资料不符合预期: %+v", profile)
			}
		})
	}
}

func TestUserProfileUpdate(t *testing.T) {
	nickname := "新昵称"
	avatar := "https://example.com/a.png"
	bio := "  个人简介  "
	empty := "   "

	tests := []struct {
		name       string
		nickname   *string
		avatarURL  *string
		bio        *string
		wantError  error
		wantNick   string
		wantAvatar string
		wantBio    string
	}{
		{name: "全部字段为 nil", wantError: ErrEmptyProfileUpdate, wantNick: "旧昵称"},
		{name: "昵称置空", nickname: &empty, wantError: ErrInvalidNickname, wantNick: "旧昵称"},
		{name: "仅改昵称", nickname: &nickname, wantNick: "新昵称"},
		{name: "改头像与简介并去空白", avatarURL: &avatar, bio: &bio, wantNick: "旧昵称", wantAvatar: avatar, wantBio: "个人简介"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			profile := &UserProfile{UserID: 1, Nickname: "旧昵称"}
			err := profile.Update(tt.nickname, tt.avatarURL, tt.bio)
			if !errors.Is(err, tt.wantError) {
				t.Fatalf("期望错误 %v，实际 %v", tt.wantError, err)
			}
			if tt.wantError != nil {
				if profile.Nickname != tt.wantNick {
					t.Fatalf("失败路径不应修改昵称: got %q", profile.Nickname)
				}
				return
			}
			if profile.Nickname != tt.wantNick || profile.AvatarURL != tt.wantAvatar || profile.Bio != tt.wantBio {
				t.Fatalf("更新结果不符合预期: %+v", profile)
			}
		})
	}
}
