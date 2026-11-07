// entity.go 定义 API 服务的配置结构体，字段与 configs 下的 YAML 键一一对应。
package config

import "time"

// Config 是配置文件的根结构。
type Config struct {
	Port     int            `yaml:"port"`
	JWT      JWTConfig      `yaml:"jwt"`
	Internal InternalConfig `yaml:"internal"`
	Database DatabaseConfig `yaml:"database"`
	Redis    RedisConfig    `yaml:"redis"`
	RabbitMQ RabbitMQConfig `yaml:"rabbitmq"`
}

// JWTConfig 描述令牌签发参数。
type JWTConfig struct {
	Secret    string        `yaml:"secret"`
	AccessTTL time.Duration `yaml:"access_ttl"`
}

// InternalConfig 描述内部接口鉴权参数。
type InternalConfig struct {
	Token string `yaml:"token"`
}

// DatabaseConfig 描述 MySQL 连接参数。
type DatabaseConfig struct {
	Host     string `yaml:"host"`
	Port     int    `yaml:"port"`
	User     string `yaml:"user"`
	Password string `yaml:"password"`
	Name     string `yaml:"name"`
}

// RedisConfig 描述 Redis 连接参数。
type RedisConfig struct {
	Addr     string `yaml:"addr"`
	Password string `yaml:"password"`
	DB       int    `yaml:"db"`
}

// RabbitMQConfig 描述 RabbitMQ 连接与三组交换机/队列/路由键。
type RabbitMQConfig struct {
	URL string `yaml:"url"`

	ClipExchange         string `yaml:"clip_exchange"`
	ClipPublishedQueue   string `yaml:"clip_published_queue"`
	ClipPublishedRouting string `yaml:"clip_published_routing"`
	ClipEmbeddingQueue   string `yaml:"clip_embedding_queue"`

	InteractionExchange  string `yaml:"interaction_exchange"`
	ActionChangedQueue   string `yaml:"action_changed_queue"`
	ActionChangedRouting string `yaml:"action_changed_routing"`

	ImpressionExchange       string `yaml:"impression_exchange"`
	PlayEventRecordedQueue   string `yaml:"play_event_recorded_queue"`
	PlayEventRecordedRouting string `yaml:"play_event_recorded_routing"`
}
