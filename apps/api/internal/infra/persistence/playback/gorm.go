// gorm.go 实现 playback 领域仓储端口：配置 upsert 与 QoS 幂等插入。
package infraplayback

import (
	"context"

	domplayback "pulsefeed/internal/domain/playback"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Repository 是 playback 领域仓储的 GORM 实现。
type Repository struct {
	db *gorm.DB
}

// New 创建 playback 仓储实例。
func New(db *gorm.DB) *Repository {
	return &Repository{db: db}
}

// GetOrCreateConfig 按用户 ID 读取配置；未命中时返回默认值（不落库）。
func (r *Repository) GetOrCreateConfig(ctx context.Context, userID int64) (*domplayback.Config, error) {
	if userID <= 0 {
		return nil, domplayback.ErrInvalidConfigUser
	}
	var row ConfigModel
	err := r.db.WithContext(ctx).Where("user_id = ?", userID).First(&row).Error
	if err == gorm.ErrRecordNotFound {
		cfg := &domplayback.Config{UserID: userID}
		cfg.Normalize()
		return cfg, nil
	}
	if err != nil {
		return nil, err
	}
	cfg := &domplayback.Config{
		UserID:        row.UserID,
		PreloadCount:  row.PreloadCount,
		QoSSampleRate: row.QoSSampleRate,
		UpdatedAt:     row.UpdatedAt,
	}
	cfg.Normalize()
	return cfg, nil
}

// UpsertConfig 写入或更新用户配置；返回最终落库值（经 Normalize）。
func (r *Repository) UpsertConfig(ctx context.Context, cfg *domplayback.Config) (*domplayback.Config, error) {
	if cfg == nil || cfg.UserID <= 0 {
		return nil, domplayback.ErrInvalidConfigUser
	}
	cfg.Normalize()
	row := ConfigModel{
		UserID:        cfg.UserID,
		PreloadCount:  cfg.PreloadCount,
		QoSSampleRate: cfg.QoSSampleRate,
	}
	res := r.db.WithContext(ctx).Save(&row)
	if res.Error != nil {
		return nil, res.Error
	}
	cfg.UpdatedAt = row.UpdatedAt
	return cfg, nil
}

// CreateQoS 走 ON CONFLICT DO NOTHING 幂等插入；inserted=true 仅当新行落库。
func (r *Repository) CreateQoS(ctx context.Context, report *domplayback.QoSReport) (bool, error) {
	if report == nil {
		return false, domplayback.ErrInvalidReportTarget
	}
	if err := report.Validate(); err != nil {
		return false, err
	}
	row := QoSReportModel{
		ReporterID: report.ReporterID,
		ClipID:     report.ClipID,
		EventID:    report.EventID,
		LatencyMS:  report.LatencyMS,
		StallCount: report.StallCount,
		Completed:  report.Completed,
		ClientInfo: report.ClientInfo,
	}
	res := r.db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&row)
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected == 1, nil
}
