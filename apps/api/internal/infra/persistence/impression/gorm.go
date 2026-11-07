// gorm.go 实现展示领域仓储端口：事件按 event_id 幂等写入，
// 首次写入时按事件类型更新当日聚合，并仅在 play 事件累加 pf_clip_stats.play_count；
// Worker 可二次调用 UpsertImpression 做补偿。
package infraimpression

import (
	"context"
	"errors"
	"time"

	domclip "pulsefeed/internal/domain/clip"
	domimpression "pulsefeed/internal/domain/impression"
	infraclip "pulsefeed/internal/infra/persistence/clip"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Repository 是曝光仓储的 GORM 实现，db 由组合根装配时注入。
type Repository struct {
	db *gorm.DB
}

// 编译期断言：确保实现满足领域端口。
var _ domimpression.Repository = (*Repository)(nil)

// New 创建曝光仓储实现。
func New(db *gorm.DB) *Repository {
	return &Repository{db: db}
}

// RecordPlayEvent 幂等写入展示/播放事件：同 event_id 已存在时为 no-op；
// 仅首次写入时按事件类型更新聚合行，并把事件标记为已聚合。
func (r *Repository) RecordPlayEvent(ctx context.Context, event *domimpression.PlayEvent) (bool, error) {
	var isNew bool
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 校验 clip 存在且为发布态，与互动链路保持一致。
		if err := lockPublishedClip(tx, event.ClipID); err != nil {
			return err
		}

		model := PlayEventModel{
			EventID:        event.EventID,
			ViewerID:       event.ViewerID,
			ClipID:         event.ClipID,
			EventType:      string(event.EventType),
			Scene:          event.Scene,
			RequestID:      event.RequestID,
			WatchMs:        event.WatchMs,
			PlayDurationMs: event.WatchMs, // 旧列继续写入同一时长，保证旧查询可读。
			Completed:      event.Completed,
			Impression:     event.Impression,
			Synced:         true, // API 同步链路在同一事务内完成聚合，直接标记已同步。
		}
		// ON CONFLICT DO NOTHING：唯一键 event_id 命中时不写入、不报错。
		result := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&model)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			// 已存在同 event_id 的记录：幂等命中，不再重复聚合。
			isNew = false
			return nil
		}

		// 新事件：按事件类型同步更新当日聚合；只有 play 事件累加 clip 播放计数。
		if err := upsertImpression(tx, event); err != nil {
			return err
		}
		if event.EventType == domimpression.EventTypePlay {
			if err := incrementPlayCount(tx, event.EventID, event.ClipID); err != nil {
				return err
			}
		}
		isNew = true
		return nil
	})
	if err != nil {
		return false, err
	}
	return isNew, nil
}

// UpsertImpression 按事件类型把单条事件的贡献累加进 (viewer_id, clip_id, day) 聚合行；
// 由 Worker 在补偿链路调用，幂等安全依赖调用方先判定事件是否已聚合。
func (r *Repository) UpsertImpression(ctx context.Context, event *domimpression.PlayEvent) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return upsertImpression(tx, event)
	})
}

// upsertImpression 执行聚合行 upsert：
// 首次插入时 first/last 均为事件发生时间；冲突时 last_played_at 取较晚值、first_played_at 保持最早值。
// 计数口径：只有 play 累加 play_count，只有 complete（或带完成标记的 play）累加 completed_count；
// exposed/skip 只登记展示时间，确保曝光去重有依据但不污染播放计数。
func upsertImpression(tx *gorm.DB, event *domimpression.PlayEvent) error {
	day := domimpression.Day(event.CreatedAt)
	playDelta := int64(0)
	completedDelta := int64(0)
	switch event.EventType {
	case domimpression.EventTypePlay:
		playDelta = 1
		if event.Completed {
			completedDelta = 1
		}
	case domimpression.EventTypeComplete:
		completedDelta = 1
	}
	return tx.Exec(`
		INSERT INTO pf_impressions
			(viewer_id, clip_id, day, play_count, completed_count, first_played_at, last_played_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON DUPLICATE KEY UPDATE
			play_count = play_count + ?,
			completed_count = completed_count + ?,
			last_played_at = GREATEST(last_played_at, ?)
	`, event.ViewerID, event.ClipID, day, playDelta, completedDelta, event.CreatedAt, event.CreatedAt,
		playDelta, completedDelta, event.CreatedAt).Error
}

// incrementPlayCount 按「播放开始」口径累加 pf_clip_stats.play_count：
// 同一 event_id 已在 QoS 上报表出现时视为同一次播放的重复信号，不再二次计数；
// 计数行随 clip 发布创建，UPDATE 影响 0 行说明 clip 未登记统计，按无副作用处理。
func incrementPlayCount(tx *gorm.DB, eventID string, clipID int64) error {
	var qosRows int64
	if err := tx.Table("pf_playback_qos_reports").
		Where("event_id = ? AND clip_id = ?", eventID, clipID).
		Count(&qosRows).Error; err != nil {
		return err
	}
	if qosRows > 0 {
		return nil
	}
	return tx.Table("pf_clip_stats").
		Where("clip_id = ?", clipID).
		UpdateColumn("play_count", gorm.Expr("play_count + 1")).Error
}

// MarkEventSynced 把播放事件标记为已完成曝光聚合。
func (r *Repository) MarkEventSynced(ctx context.Context, eventID string) error {
	return r.db.WithContext(ctx).Model(&PlayEventModel{}).
		Where("event_id = ?", eventID).
		Update("synced", true).Error
}

// IsEventSynced 查询播放事件是否已完成曝光聚合；事件不存在时返回 false 而非错误。
func (r *Repository) IsEventSynced(ctx context.Context, eventID string) (bool, error) {
	var model PlayEventModel
	err := r.db.WithContext(ctx).Select("synced").Where("event_id = ?", eventID).Take(&model).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, nil
		}
		return false, err
	}
	return model.Synced, nil
}

// lockPublishedClip 以 FOR UPDATE 锁定 clip 行并校验其存在且为发布态。
func lockPublishedClip(tx *gorm.DB, clipID int64) error {
	var clip infraclip.ClipModel
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Select("id", "status").Where("id = ?", clipID).Take(&clip).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return domimpression.ErrClipNotFound
	}
	if err != nil {
		return err
	}
	if clip.Status != domclip.StatusPublished {
		return domimpression.ErrClipNotFound
	}
	return nil
}

// dayNow 返回当前本地日字符串，供外部按需调用。
func dayNow() string {
	return time.Now().Format("2006-01-02")
}
