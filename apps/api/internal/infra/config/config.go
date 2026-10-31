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

	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("校验配置文件 %q 失败: %w", path, err)
	}

	return cfg, nil
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

	requireString("internal.token", c.Internal.Token)

	requireString("database.host", c.Database.Host)
	requirePositive("database.port", c.Database.Port)
	requireString("database.user", c.Database.User)
	requireString("database.password", c.Database.Password)
	requireString("database.name", c.Database.Name)

	requireString("redis.addr", c.Redis.Addr)

	requireString("rabbitmq.url", c.RabbitMQ.URL)
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

	return nil
}
