// config.go 负责从 YAML 文件加载配置并校验必填项，缺失时直接返回错误。
package config

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/goccy/go-yaml"
)

// Load 读取指定路径的 YAML 配置，解析并校验后返回配置实例。
func Load(path string) (*Config, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取配置文件 %q 失败: %w", path, err)
	}

	cfg := &Config{}
	if err := yaml.Unmarshal(content, cfg); err != nil {
		return nil, fmt.Errorf("解析配置文件 %q 失败: %w", path, err)
	}

	// 密钥优先取环境变量，便于容器与 CI 注入而不把真实密钥写进仓库。
	if secret := os.Getenv("PULSEFEED_JWT_SECRET"); secret != "" {
		cfg.JWT.Secret = secret
	}

	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("校验配置文件 %q 失败: %w", path, err)
	}

	return cfg, nil
}

// minJWTSecretBytes 是 HS256 密钥的最小长度，低于该长度直接拒绝启动。
const minJWTSecretBytes = 32

// placeholderSecrets 是仓库里出现过的示例值，命中即视为未配置。
var placeholderSecrets = []string{"placeholder-secret", "change-me", "pulsefeed-docker-secret"}

// validateJWTSecret 校验密钥长度与占位值；任一不满足都拒绝启动。
func validateJWTSecret(secret string) error {
	if len(secret) < minJWTSecretBytes {
		return fmt.Errorf("jwt.secret 至少 %d 字节，当前 %d 字节", minJWTSecretBytes, len(secret))
	}
	lower := strings.ToLower(secret)
	for _, placeholder := range placeholderSecrets {
		if lower == placeholder || strings.Contains(lower, "placeholder") {
			return fmt.Errorf("jwt.secret 仍是占位值，请通过 PULSEFEED_JWT_SECRET 注入真实密钥")
		}
	}
	return nil
}

// validate 检查所有必填配置项，任一项缺失或非法都会返回错误。
func (c *Config) validate() error {
	missing := make([]string, 0, 24)

	requireString := func(name, value string) {
		if value == "" {
			missing = append(missing, name)
		}
	}
	requirePositive := func(name string, value int) {
		if value <= 0 {
			missing = append(missing, name)
		}
	}
	requireDuration := func(name string, value time.Duration) {
		if value <= 0 {
			missing = append(missing, name)
		}
	}

	requirePositive("port", c.Port)

	requireString("jwt.secret", c.JWT.Secret)
	requireDuration("jwt.access_ttl", c.JWT.AccessTTL)
	requireDuration("jwt.refresh_ttl", c.JWT.RefreshTTL)
	requireDuration("jwt.refresh_grace", c.JWT.RefreshGrace)
	requireDuration("jwt.leeway", c.JWT.Leeway)

	requireString("internal.token", c.Internal.Token)

	requireString("database.host", c.Database.Host)
	requirePositive("database.port", c.Database.Port)
	requireString("database.user", c.Database.User)
	requireString("database.password", c.Database.Password)
	requireString("database.name", c.Database.Name)

	// redis.addr 允许为空：空 addr 表示禁用缓存，Feed 等链路降级为纯 DB（S8 验收要求）。

	// rabbitmq.url 允许为空：空 url 表示禁用 MQ，互动链路降级为同步落库（S10 验收要求）；
	// Worker 组合根会额外校验 url 非空。
	requireString("rabbitmq.clip_exchange", c.RabbitMQ.ClipExchange)
	requireString("rabbitmq.clip_published_queue", c.RabbitMQ.ClipPublishedQueue)
	requireString("rabbitmq.clip_published_routing", c.RabbitMQ.ClipPublishedRouting)
	requireString("rabbitmq.clip_embedding_queue", c.RabbitMQ.ClipEmbeddingQueue)
	requireString("rabbitmq.interaction_exchange", c.RabbitMQ.InteractionExchange)
	requireString("rabbitmq.action_changed_queue", c.RabbitMQ.ActionChangedQueue)
	requireString("rabbitmq.action_changed_routing", c.RabbitMQ.ActionChangedRouting)
	requireString("rabbitmq.impression_exchange", c.RabbitMQ.ImpressionExchange)
	requireString("rabbitmq.play_event_recorded_queue", c.RabbitMQ.PlayEventRecordedQueue)
	requireString("rabbitmq.play_event_recorded_routing", c.RabbitMQ.PlayEventRecordedRouting)

	if len(missing) > 0 {
		return fmt.Errorf("缺少必填配置项: %s", strings.Join(missing, ", "))
	}

	if err := validateJWTSecret(c.JWT.Secret); err != nil {
		return err
	}

	return nil
}
