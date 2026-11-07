// gorm.go 实现 message 领域仓储端口：唯一索引 uk_event_id 幂等、未读计数、标记已读。
package inframessage

import (
	"context"

	dommessage "pulsefeed/internal/domain/message"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Repository 是 message 领域仓储的 GORM 实现。
type Repository struct {
	db *gorm.DB
}

// New 创建 message 仓储实例。
func New(db *gorm.DB) *Repository {
	return &Repository{db: db}
}

// Create 走 ON CONFLICT DO NOTHING 实现 uk_event_id 幂等；inserted 仅当新行落库时为 true。
func (r *Repository) Create(ctx context.Context, msg *dommessage.Message) (bool, error) {
	if msg.ReceiverID <= 0 {
		return false, dommessage.ErrInvalidReceiver
	}
	if msg.EventID == "" {
		return false, dommessage.ErrEventIDEmpty
	}
	m := &MessageModel{
		ReceiverID: msg.ReceiverID,
		ActorID:    msg.ActorID,
		Kind:       string(msg.Kind),
		TargetID:   msg.TargetID,
		EventID:    msg.EventID,
	}
	res := r.db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(m)
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected == 1, nil
}

// ListPage 按 (created_at, id) 严格递减返回 receiverID 的消息列表。
func (r *Repository) ListPage(ctx context.Context, receiverID int64, cursor *dommessage.ListCursor, limit int) ([]*dommessage.Message, error) {
	if receiverID <= 0 {
		return nil, dommessage.ErrInvalidReceiver
	}
	var rows []MessageModel
	q := r.db.WithContext(ctx).Table("pf_messages").
		Where("receiver_user_id = ?", receiverID).
		Order("created_at DESC, id DESC").
		Limit(limit)
	if cursor != nil {
		q = q.Where(
			"(created_at < ?) OR (created_at = ? AND id < ?)",
			cursor.CreatedAt, cursor.CreatedAt, cursor.ID,
		)
	}
	if err := q.Scan(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]*dommessage.Message, 0, len(rows))
	for i := range rows {
		out = append(out, &dommessage.Message{
			ID:         rows[i].ID,
			ReceiverID: rows[i].ReceiverID,
			ActorID:    rows[i].ActorID,
			Kind:       dommessage.Kind(rows[i].Kind),
			TargetID:   rows[i].TargetID,
			EventID:    rows[i].EventID,
			CreatedAt:  rows[i].CreatedAt,
		})
		if rows[i].ReadAt != nil {
			out[i].ReadAt = *rows[i].ReadAt
		}
	}
	return out, nil
}

// CountUnread 返回 receiverID 未读行数（read_at NULL）。
func (r *Repository) CountUnread(ctx context.Context, receiverID int64) (int64, error) {
	if receiverID <= 0 {
		return 0, dommessage.ErrInvalidReceiver
	}
	var n int64
	err := r.db.WithContext(ctx).Model(&MessageModel{}).
		Where("receiver_user_id = ? AND read_at IS NULL", receiverID).
		Count(&n).Error
	return n, err
}

// MarkAllRead 把 receiverID 所有未读消息一次性标记已读。
func (r *Repository) MarkAllRead(ctx context.Context, receiverID int64) error {
	if receiverID <= 0 {
		return dommessage.ErrInvalidReceiver
	}
	return r.db.WithContext(ctx).Model(&MessageModel{}).
		Where("receiver_user_id = ? AND read_at IS NULL", receiverID).
		Update("read_at", gorm.Expr("NOW()")).Error
}
