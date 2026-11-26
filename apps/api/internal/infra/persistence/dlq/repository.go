// repository.go 实现死信消息仓储：按「来源队列 + MessageId + 载荷哈希」去重落库。
package infradlq

import (
	"context"
	"crypto/sha256"
	"fmt"

	appdlq "pulsefeed/internal/application/dlq"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Repository 是死信消息仓储的 GORM 实现，db 由组合根装配时注入。
type Repository struct {
	db *gorm.DB
}

// 编译期断言：确保实现满足死信消费者的存储端口。
var _ appdlq.Store = (*Repository)(nil)

// New 创建死信消息仓储实现。
func New(db *gorm.DB) *Repository {
	return &Repository{db: db}
}

// Save 幂等记录一条死信消息：同一消息重复消费时不新增行、不报错。
func (r *Repository) Save(ctx context.Context, message appdlq.Message) error {
	record := MessageModel{
		DedupeKey:   dedupeKey(message),
		SourceQueue: message.SourceQueue,
		EventType:   message.EventType,
		EventID:     message.EventID,
		Reason:      message.Reason,
		Attempts:    message.Attempts,
		LastError:   truncate(message.LastError, 512),
		Payload:     message.Payload,
		ReceivedAt:  message.ReceivedAt,
	}
	return r.db.WithContext(ctx).
		Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "dedupe_key"}}, DoNothing: true}).
		Create(&record).Error
}

// dedupeKey 由来源队列、MessageId 与载荷哈希组成：
// 同一消息重投时内容不变，重复消费只落一行。
func dedupeKey(message appdlq.Message) string {
	sum := sha256.Sum256(message.Payload)
	messageID := message.MessageID
	if messageID == "" {
		messageID = "no-message-id"
	}
	return fmt.Sprintf("%s|%s|%x", message.SourceQueue, messageID, sum)
}

// truncate 按字符截断错误信息，避免超出列宽。
func truncate(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit])
}
