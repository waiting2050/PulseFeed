// event.go 定义 clip.published 事件载荷与事件发布端口，应用层不感知 MQ 实现。
package appclip

import (
	"context"
	"time"
)

// ClipPublished 是 clip 发布成功后投递的事件载荷，供扇出/向量等下游消费。
type ClipPublished struct {
	ClipID      int64     `json:"clip_id"`
	CreatorID   int64     `json:"creator_id"`
	PublishedAt time.Time `json:"published_at"`
}

// Publisher 是事件发布端口，由 infra/mq 实现并经组合根注入；未注入时发布流程跳过投递。
type Publisher interface {
	// PublishClipPublished 投递 clip.published 事件。
	PublishClipPublished(ctx context.Context, event ClipPublished) error
}
