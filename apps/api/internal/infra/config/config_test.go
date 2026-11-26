// config_test.go 覆盖敏感配置的环境变量覆盖与启动校验：
// 内部接口令牌必须非空、足够长且不是占位值，两套 YAML 都要能通过校验。
package config

import (
	"strings"
	"testing"
)

// TestApplyEnvOverrides 验证优先级为“环境变量 > YAML”，环境变量为空时保留 YAML 值。
func TestApplyEnvOverrides(t *testing.T) {
	t.Run("环境变量覆盖 YAML", func(t *testing.T) {
		t.Setenv("PULSEFEED_JWT_SECRET", strings.Repeat("j", 40))
		t.Setenv("PULSEFEED_INTERNAL_TOKEN", strings.Repeat("i", 40))
		t.Setenv("PULSEFEED_FEED_CURSOR_SECRET", strings.Repeat("c", 40))

		cfg := &Config{}
		cfg.JWT.Secret = "yaml-jwt"
		cfg.Internal.Token = "yaml-internal"
		cfg.Feed.CursorSecret = "yaml-cursor"
		applyEnvOverrides(cfg)

		if cfg.JWT.Secret != strings.Repeat("j", 40) {
			t.Fatalf("jwt.secret 应被环境变量覆盖，实际 %q", cfg.JWT.Secret)
		}
		if cfg.Internal.Token != strings.Repeat("i", 40) {
			t.Fatalf("internal.token 应被环境变量覆盖，实际 %q", cfg.Internal.Token)
		}
		if cfg.Feed.CursorSecret != strings.Repeat("c", 40) {
			t.Fatalf("feed.cursor_secret 应被环境变量覆盖，实际 %q", cfg.Feed.CursorSecret)
		}
	})

	t.Run("环境变量为空时保留 YAML", func(t *testing.T) {
		t.Setenv("PULSEFEED_JWT_SECRET", "")
		t.Setenv("PULSEFEED_INTERNAL_TOKEN", "")
		t.Setenv("PULSEFEED_FEED_CURSOR_SECRET", "")

		cfg := &Config{}
		cfg.JWT.Secret = "yaml-jwt"
		cfg.Internal.Token = "yaml-internal"
		cfg.Feed.CursorSecret = "yaml-cursor"
		applyEnvOverrides(cfg)

		if cfg.JWT.Secret != "yaml-jwt" || cfg.Internal.Token != "yaml-internal" || cfg.Feed.CursorSecret != "yaml-cursor" {
			t.Fatalf("空环境变量不应覆盖 YAML: jwt=%q internal=%q cursor=%q",
				cfg.JWT.Secret, cfg.Internal.Token, cfg.Feed.CursorSecret)
		}
	})
}

// TestValidateFeedCursorSecret 验证游标签名密钥的非空、长度与占位值校验。
func TestValidateFeedCursorSecret(t *testing.T) {
	tests := []struct {
		name    string
		secret  string
		wantErr string
	}{
		{name: "为空", secret: "", wantErr: "PULSEFEED_FEED_CURSOR_SECRET"},
		{name: "占位值", secret: strings.Repeat("placeholder-", 4), wantErr: "占位值"},
		{name: "长度不足", secret: strings.Repeat("a", minCursorSecretBytes-1), wantErr: "至少 32 字节"},
		{name: "合法密钥", secret: strings.Repeat("a", minCursorSecretBytes)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateFeedCursorSecret(tt.secret)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("合法密钥不应报错: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("期望错误包含 %q，实际 %v", tt.wantErr, err)
			}
		})
	}
}

// TestValidateInternalToken 验证非空、长度与占位值三类校验及提示文案。
func TestValidateInternalToken(t *testing.T) {
	tests := []struct {
		name    string
		token   string
		wantErr string
	}{
		{name: "为空", token: "", wantErr: "PULSEFEED_INTERNAL_TOKEN"},
		{name: "占位值", token: "placeholder-internal-token", wantErr: "占位值"},
		{name: "历史示例值", token: "pulsefeed-internal-token", wantErr: "占位值"},
		{name: "长度不足", token: strings.Repeat("a", minInternalTokenBytes-1), wantErr: "至少 32 字节"},
		{name: "合法令牌", token: strings.Repeat("a", minInternalTokenBytes)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateInternalToken(tt.token)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("合法令牌不应报错: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("期望错误包含 %q，实际 %v", tt.wantErr, err)
			}
		})
	}
}

// TestLoadConfigsPassValidation 验证仓库内两套 YAML 都能通过启动校验，避免示例值残留导致启动失败。
func TestLoadConfigsPassValidation(t *testing.T) {
	t.Setenv("PULSEFEED_JWT_SECRET", "")
	t.Setenv("PULSEFEED_INTERNAL_TOKEN", "")
	t.Setenv("PULSEFEED_FEED_CURSOR_SECRET", "")

	for _, path := range []string{"../../../configs/config.yaml", "../../../configs/config.docker.yaml"} {
		cfg, err := Load(path)
		if err != nil {
			t.Fatalf("加载 %s 失败: %v", path, err)
		}
		if err := validateInternalToken(cfg.Internal.Token); err != nil {
			t.Fatalf("%s 的 internal.token 未通过校验: %v", path, err)
		}
	}
}
