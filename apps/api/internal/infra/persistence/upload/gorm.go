// gorm.go 实现上传领域仓储端口：所有状态迁移都用带条件的 UPDATE 表达，
// 影响行数即 CAS 是否成功的唯一判据，供合并/取消/过期竞争时分流。
package infraupload

import (
	"context"
	"errors"
	"time"

	domupload "pulsefeed/internal/domain/upload"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Repository 是上传仓储的 GORM 实现，db 由组合根装配时注入。
type Repository struct {
	db *gorm.DB
}

// 编译期断言：确保实现满足领域端口。
var _ domupload.Repository = (*Repository)(nil)

// New 创建上传仓储实现。
func New(db *gorm.DB) *Repository {
	return &Repository{db: db}
}

// CreateSession 新建一条 pending 会话。
func (r *Repository) CreateSession(ctx context.Context, session *domupload.Session) error {
	now := time.Now()
	model := SessionModel{
		UploadID:   session.UploadID,
		UserID:     session.UserID,
		Kind:       session.Kind,
		Filename:   session.Filename,
		SizeBytes:  session.SizeBytes,
		ChunkSize:  int(session.ChunkSize),
		TotalParts: session.TotalParts,
		Status:     int8(session.Status),
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	if session.SHA256 != "" {
		sha := session.SHA256
		model.SHA256 = &sha
	}
	if err := r.db.WithContext(ctx).Create(&model).Error; err != nil {
		return err
	}
	session.CreatedAt = now
	session.UpdatedAt = now
	return nil
}

// GetSession 按 upload_id 读取会话；未命中返回领域哨兵错误。
func (r *Repository) GetSession(ctx context.Context, uploadID string) (*domupload.Session, error) {
	var model SessionModel
	err := r.db.WithContext(ctx).Where("upload_id = ?", uploadID).Take(&model).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, domupload.ErrSessionNotFound
	}
	if err != nil {
		return nil, err
	}
	return toSession(&model), nil
}

// FindReusable 查找可复用的 pending 会话：同用户、同文件名、同大小且未过期。
// 未命中统一返回 ErrSessionNotFound，复用与非复用共用同一错误语义。
func (r *Repository) FindReusable(ctx context.Context, userID int64, filename string, sizeBytes int64, updatedAfter time.Time) (*domupload.Session, error) {
	var model SessionModel
	err := r.db.WithContext(ctx).
		Where("user_id = ? AND filename = ? AND size_bytes = ? AND status = ? AND updated_at >= ?",
			userID, filename, sizeBytes, int8(domupload.StatusPending), updatedAfter).
		Order("id DESC").Take(&model).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, domupload.ErrSessionNotFound
	}
	if err != nil {
		return nil, err
	}
	return toSession(&model), nil
}

// CountActive 统计该用户处于 pending / merging 的会话数。
func (r *Repository) CountActive(ctx context.Context, userID int64) (int, error) {
	var count int64
	err := r.db.WithContext(ctx).Model(&SessionModel{}).
		Where("user_id = ? AND status IN ?", userID,
			[]int8{int8(domupload.StatusPending), int8(domupload.StatusMerging)}).
		Count(&count).Error
	return int(count), err
}

// ListStale 返回超期的 pending 会话与卡住的 merging 会话。
func (r *Repository) ListStale(ctx context.Context, pendingBefore, mergingBefore time.Time) ([]domupload.Session, error) {
	var models []SessionModel
	err := r.db.WithContext(ctx).
		Where("(status = ? AND updated_at < ?) OR (status = ? AND updated_at < ?)",
			int8(domupload.StatusPending), pendingBefore,
			int8(domupload.StatusMerging), mergingBefore).
		Find(&models).Error
	if err != nil {
		return nil, err
	}
	sessions := make([]domupload.Session, 0, len(models))
	for i := range models {
		sessions = append(sessions, *toSession(&models[i]))
	}
	return sessions, nil
}

// ExpireIfStale 条件置过期：仅当仍为 pending/merging 且 updated_at 早于 staleBefore。
func (r *Repository) ExpireIfStale(ctx context.Context, uploadID string, staleBefore time.Time) (bool, error) {
	result := r.db.WithContext(ctx).Model(&SessionModel{}).
		Where("upload_id = ? AND status IN ? AND updated_at < ?", uploadID,
			[]int8{int8(domupload.StatusPending), int8(domupload.StatusMerging)}, staleBefore).
		UpdateColumns(map[string]any{
			"status":     int8(domupload.StatusExpired),
			"updated_at": time.Now(),
		})
	return result.RowsAffected == 1, result.Error
}

// FailIfMergingStale 条件置失败：仅当仍为 merging 且 updated_at 早于 staleBefore。
func (r *Repository) FailIfMergingStale(ctx context.Context, uploadID string, staleBefore time.Time) (bool, error) {
	result := r.db.WithContext(ctx).Model(&SessionModel{}).
		Where("upload_id = ? AND status = ? AND updated_at < ?", uploadID, int8(domupload.StatusMerging), staleBefore).
		UpdateColumns(map[string]any{
			"status":        int8(domupload.StatusFailed),
			"error_message": "合并超时",
			"updated_at":    time.Now(),
		})
	return result.RowsAffected == 1, result.Error
}

// MarkMerging 条件迁移 pending → merging。
func (r *Repository) MarkMerging(ctx context.Context, uploadID string) (bool, error) {
	result := r.db.WithContext(ctx).Model(&SessionModel{}).
		Where("upload_id = ? AND status = ?", uploadID, int8(domupload.StatusPending)).
		UpdateColumns(map[string]any{
			"status":     int8(domupload.StatusMerging),
			"updated_at": time.Now(),
		})
	return result.RowsAffected == 1, result.Error
}

// MarkPending 条件回滚 merging → pending，用于分片不齐时恢复可写状态。
func (r *Repository) MarkPending(ctx context.Context, uploadID string) (bool, error) {
	result := r.db.WithContext(ctx).Model(&SessionModel{}).
		Where("upload_id = ? AND status = ?", uploadID, int8(domupload.StatusMerging)).
		UpdateColumns(map[string]any{
			"status":     int8(domupload.StatusPending),
			"updated_at": time.Now(),
		})
	return result.RowsAffected == 1, result.Error
}

// MarkCompleted 条件迁移 merging → completed 并写入落位地址与时长。
func (r *Repository) MarkCompleted(ctx context.Context, uploadID, url string, durationMS int64) (bool, error) {
	result := r.db.WithContext(ctx).Model(&SessionModel{}).
		Where("upload_id = ? AND status = ?", uploadID, int8(domupload.StatusMerging)).
		UpdateColumns(map[string]any{
			"status":      int8(domupload.StatusCompleted),
			"url":         url,
			"duration_ms": durationMS,
			"updated_at":  time.Now(),
		})
	return result.RowsAffected == 1, result.Error
}

// RevertCompletedToFailed 条件回滚 completed → failed，用于 rename 失败时撤销已完成标记。
func (r *Repository) RevertCompletedToFailed(ctx context.Context, uploadID, reason string) (bool, error) {
	result := r.db.WithContext(ctx).Model(&SessionModel{}).
		Where("upload_id = ? AND status = ?", uploadID, int8(domupload.StatusCompleted)).
		UpdateColumns(map[string]any{
			"status":        int8(domupload.StatusFailed),
			"error_message": reason,
			"updated_at":    time.Now(),
		})
	return result.RowsAffected == 1, result.Error
}

// MarkFailed 条件迁移 pending/merging → failed 并记录原因。
func (r *Repository) MarkFailed(ctx context.Context, uploadID, reason string) (bool, error) {
	result := r.db.WithContext(ctx).Model(&SessionModel{}).
		Where("upload_id = ? AND status IN ?", uploadID,
			[]int8{int8(domupload.StatusPending), int8(domupload.StatusMerging)}).
		UpdateColumns(map[string]any{
			"status":        int8(domupload.StatusFailed),
			"error_message": truncateReason(reason),
			"updated_at":    time.Now(),
		})
	return result.RowsAffected == 1, result.Error
}

// MarkCanceled 条件迁移 pending/merging → canceled。
func (r *Repository) MarkCanceled(ctx context.Context, uploadID string) (bool, error) {
	result := r.db.WithContext(ctx).Model(&SessionModel{}).
		Where("upload_id = ? AND status IN ?", uploadID,
			[]int8{int8(domupload.StatusPending), int8(domupload.StatusMerging)}).
		UpdateColumns(map[string]any{
			"status":     int8(domupload.StatusCanceled),
			"updated_at": time.Now(),
		})
	return result.RowsAffected == 1, result.Error
}

// UpdateSHA256IfEmpty 仅在会话 sha256 为空时回填客户端期望值。
func (r *Repository) UpdateSHA256IfEmpty(ctx context.Context, uploadID, sha256 string) error {
	return r.db.WithContext(ctx).Model(&SessionModel{}).
		Where("upload_id = ? AND (sha256 IS NULL OR sha256 = '')", uploadID).
		UpdateColumns(map[string]any{"sha256": sha256}).Error
}

// UpdateCoverURL 写入封面地址；调用方按可降级处理。
func (r *Repository) UpdateCoverURL(ctx context.Context, uploadID, coverURL string) error {
	return r.db.WithContext(ctx).Model(&SessionModel{}).
		Where("upload_id = ?", uploadID).
		UpdateColumns(map[string]any{"cover_url": coverURL}).Error
}

// UpsertPart 幂等写入分片元数据并返回当前已收分片数。
// 同分片重复上传只覆盖大小与时间，计数按行数统计不会重复累加。
func (r *Repository) UpsertPart(ctx context.Context, uploadID string, partNumber int, sizeBytes int64) (int, error) {
	now := time.Now()
	err := r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "upload_id"}, {Name: "part_number"}},
		DoUpdates: clause.Assignments(map[string]any{"size_bytes": sizeBytes, "created_at": now}),
	}).Create(&PartModel{
		UploadID:   uploadID,
		PartNumber: partNumber,
		SizeBytes:  int(sizeBytes),
		CreatedAt:  now,
	}).Error
	if err != nil {
		return 0, err
	}
	var count int64
	if err := r.db.WithContext(ctx).Model(&PartModel{}).Where("upload_id = ?", uploadID).Count(&count).Error; err != nil {
		return 0, err
	}
	return int(count), nil
}

// ListPartNumbers 按分片号升序返回已收分片号。
func (r *Repository) ListPartNumbers(ctx context.Context, uploadID string) ([]int, error) {
	numbers := make([]int, 0)
	err := r.db.WithContext(ctx).Model(&PartModel{}).
		Where("upload_id = ?", uploadID).
		Order("part_number ASC").
		Pluck("part_number", &numbers).Error
	if err != nil {
		return nil, err
	}
	return numbers, nil
}

// ActiveUploadIDs 返回仍处于 pending/merging 的会话 ID 集合，供孤儿目录回收。
func (r *Repository) ActiveUploadIDs(ctx context.Context) (map[string]struct{}, error) {
	ids := make([]string, 0)
	err := r.db.WithContext(ctx).Model(&SessionModel{}).
		Where("status IN ?", []int8{int8(domupload.StatusPending), int8(domupload.StatusMerging)}).
		Pluck("upload_id", &ids).Error
	if err != nil {
		return nil, err
	}
	active := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		active[id] = struct{}{}
	}
	return active, nil
}

// DeleteTerminalBefore 删除早于 before 的终态会话行与其分片行。
func (r *Repository) DeleteTerminalBefore(ctx context.Context, before time.Time) error {
	terminal := []int8{
		int8(domupload.StatusCompleted), int8(domupload.StatusCanceled),
		int8(domupload.StatusFailed), int8(domupload.StatusExpired),
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		sub := tx.Model(&SessionModel{}).Select("upload_id").
			Where("status IN ? AND updated_at < ?", terminal, before)
		if err := tx.Where("upload_id IN (?)", sub).Delete(&PartModel{}).Error; err != nil {
			return err
		}
		return tx.Where("status IN ? AND updated_at < ?", terminal, before).
			Delete(&SessionModel{}).Error
	})
}

// toSession 把持久化模型转换为领域实体。
func toSession(model *SessionModel) *domupload.Session {
	session := &domupload.Session{
		UploadID:     model.UploadID,
		UserID:       model.UserID,
		Kind:         model.Kind,
		Filename:     model.Filename,
		SizeBytes:    model.SizeBytes,
		ChunkSize:    int64(model.ChunkSize),
		TotalParts:   model.TotalParts,
		Status:       domupload.Status(model.Status),
		URL:          model.URL,
		CoverURL:     model.CoverURL,
		DurationMS:   model.DurationMS,
		ErrorMessage: model.ErrorMessage,
		CreatedAt:    model.CreatedAt,
		UpdatedAt:    model.UpdatedAt,
	}
	if model.SHA256 != nil {
		session.SHA256 = *model.SHA256
	}
	return session
}

// truncateReason 把失败原因限制在列宽内，避免超长错误写库失败。
func truncateReason(reason string) string {
	const max = 255
	runes := []rune(reason)
	if len(runes) <= max {
		return reason
	}
	return string(runes[:max])
}
