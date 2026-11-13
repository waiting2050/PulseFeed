// session.go 实现刷新令牌的 GORM 仓储：登录建链、NOWAIT 事务轮换、按链/按用户吊销。
package infraaccount

import (
	"context"
	"errors"
	"time"

	domainaccount "pulsefeed/internal/domain/account"

	"github.com/go-sql-driver/mysql"
	"gorm.io/gorm"
)

// RefreshTokenModel 映射 pf_refresh_tokens 表，token_hash 上建唯一索引，轮换与宽限查表都走它。
type RefreshTokenModel struct {
	ID            int64      `gorm:"column:id;primaryKey;autoIncrement"`
	UserID        int64      `gorm:"column:user_id;not null;index:idx_user_family,priority:1"`
	FamilyID      string     `gorm:"column:family_id;type:char(36);not null;index:idx_user_family,priority:2"`
	TokenHash     string     `gorm:"column:token_hash;type:char(64);not null;uniqueIndex:uk_token_hash"`
	RotatedFrom   int64      `gorm:"column:rotated_from;not null;default:0"`
	DeviceLabel   string     `gorm:"column:device_label;size:128;not null;default:''"`
	IssuedAt      time.Time  `gorm:"column:issued_at;not null"`
	ExpiresAt     time.Time  `gorm:"column:expires_at;not null;index:idx_expires_at"`
	RevokedAt     *time.Time `gorm:"column:revoked_at"`
	RevokedReason string     `gorm:"column:revoked_reason;size:32;not null;default:''"`
}

// TableName 指定刷新令牌表名。
func (RefreshTokenModel) TableName() string {
	return "pf_refresh_tokens"
}

// SessionRepository 是 RefreshToken 会话链的 GORM 实现。
type SessionRepository struct {
	db *gorm.DB
}

// 编译期断言：确保实现满足领域端口。
var _ domainaccount.SessionRepository = (*SessionRepository)(nil)

// NewSession 创建刷新令牌仓储。
func NewSession(db *gorm.DB) *SessionRepository {
	return &SessionRepository{db: db}
}

// Create 插入一条新链的起始令牌。
func (r *SessionRepository) Create(ctx context.Context, token *domainaccount.RefreshToken) error {
	model := toRefreshTokenModel(token)
	if err := r.db.WithContext(ctx).Create(&model).Error; err != nil {
		return err
	}
	token.ID = model.ID
	return nil
}

// FindByHash 按哈希读取一行（不过滤状态），用于限流前解析用户 ID。
func (r *SessionRepository) FindByHash(ctx context.Context, hash string) (*domainaccount.RefreshToken, error) {
	return r.findByHash(ctx, hash)
}

// FindActiveByHash 按哈希读取一行，用于宽限期回放前的二次校验。
func (r *SessionRepository) FindActiveByHash(ctx context.Context, hash string) (*domainaccount.RefreshToken, error) {
	return r.findByHash(ctx, hash)
}

// findByHash 是 FindByHash / FindActiveByHash 的共同实现，未命中返回 (nil, nil)。
func (r *SessionRepository) findByHash(ctx context.Context, hash string) (*domainaccount.RefreshToken, error) {
	var model RefreshTokenModel
	err := r.db.WithContext(ctx).Where("token_hash = ?", hash).Take(&model).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	token := toRefreshToken(&model)
	return token, nil
}

// Rotate 在单个事务内锁定旧行、判定状态、标记轮换并插入新行。
// SELECT 使用 FOR UPDATE NOWAIT：拿不到行锁时立即返回，映射为 RotateGrace 交由宽限期回放。
func (r *SessionRepository) Rotate(ctx context.Context, oldHash string, next *domainaccount.RefreshToken, grace time.Duration) (*domainaccount.RotateResult, error) {
	var result *domainaccount.RotateResult
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row RefreshTokenModel
		err := tx.Raw(
			"SELECT id, user_id, family_id, token_hash, rotated_from, device_label, issued_at, expires_at, revoked_at, revoked_reason "+
				"FROM pf_refresh_tokens WHERE token_hash = ? FOR UPDATE NOWAIT", oldHash,
		).Scan(&row).Error
		if err != nil {
			if isNoWaitLockError(err) {
				// 行锁被其他事务占用：不排队，直接进入宽限期分支（缓存未命中最终返回 40103）。
				result = &domainaccount.RotateResult{Outcome: domainaccount.RotateGrace}
				return nil
			}
			return err
		}
		if row.ID == 0 {
			result = &domainaccount.RotateResult{Outcome: domainaccount.RotateNotFound}
			return nil
		}

		now := time.Now()
		base := domainaccount.RotateResult{UserID: row.UserID, FamilyID: row.FamilyID}

		if row.RevokedAt == nil {
			if !row.ExpiresAt.After(now) {
				// 过期行标记 expired，但不牵连同一用户的其他链。
				if err := tx.Model(&RefreshTokenModel{}).Where("id = ?", row.ID).
					Updates(map[string]any{"revoked_at": now, "revoked_reason": "expired"}).Error; err != nil {
					return err
				}
				base.Outcome = domainaccount.RotateExpired
				result = &base
				return nil
			}

			if err := tx.Model(&RefreshTokenModel{}).Where("id = ?", row.ID).
				Updates(map[string]any{"revoked_at": now, "revoked_reason": "rotated"}).Error; err != nil {
				return err
			}
			next.UserID = row.UserID
			next.FamilyID = row.FamilyID
			next.RotatedFrom = row.ID
			model := toRefreshTokenModel(next)
			if err := tx.Create(&model).Error; err != nil {
				return err
			}
			next.ID = model.ID
			base.Outcome = domainaccount.RotateOK
			result = &base
			return nil
		}

		base.RevokedAt = *row.RevokedAt
		if row.RevokedReason == "rotated" && now.Sub(*row.RevokedAt) <= grace {
			base.Outcome = domainaccount.RotateGrace
		} else {
			base.Outcome = domainaccount.RotateReplay
		}
		result = &base
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// RevokeFamily 吊销指定用户指定会话链下所有未失效的行。
func (r *SessionRepository) RevokeFamily(ctx context.Context, userID int64, familyID, reason string) (int64, error) {
	result := r.db.WithContext(ctx).Model(&RefreshTokenModel{}).
		Where("user_id = ? AND family_id = ? AND revoked_at IS NULL", userID, familyID).
		Updates(map[string]any{"revoked_at": time.Now(), "revoked_reason": reason})
	if result.Error != nil {
		return 0, result.Error
	}
	return result.RowsAffected, nil
}

// RevokeAll 吊销指定用户全部未失效的行，重放处置使用。
func (r *SessionRepository) RevokeAll(ctx context.Context, userID int64, reason string) (int64, error) {
	result := r.db.WithContext(ctx).Model(&RefreshTokenModel{}).
		Where("user_id = ? AND revoked_at IS NULL", userID).
		Updates(map[string]any{"revoked_at": time.Now(), "revoked_reason": reason})
	if result.Error != nil {
		return 0, result.Error
	}
	return result.RowsAffected, nil
}

// toRefreshTokenModel 将领域对象转换为持久化模型。
func toRefreshTokenModel(token *domainaccount.RefreshToken) RefreshTokenModel {
	return RefreshTokenModel{
		ID:            token.ID,
		UserID:        token.UserID,
		FamilyID:      token.FamilyID,
		TokenHash:     token.TokenHash,
		RotatedFrom:   token.RotatedFrom,
		DeviceLabel:   token.DeviceLabel,
		IssuedAt:      token.IssuedAt,
		ExpiresAt:     token.ExpiresAt,
		RevokedAt:     token.RevokedAt,
		RevokedReason: token.RevokedReason,
	}
}

// toRefreshToken 将持久化模型转换为领域对象。
func toRefreshToken(model *RefreshTokenModel) *domainaccount.RefreshToken {
	return &domainaccount.RefreshToken{
		ID:            model.ID,
		UserID:        model.UserID,
		FamilyID:      model.FamilyID,
		TokenHash:     model.TokenHash,
		RotatedFrom:   model.RotatedFrom,
		DeviceLabel:   model.DeviceLabel,
		IssuedAt:      model.IssuedAt,
		ExpiresAt:     model.ExpiresAt,
		RevokedAt:     model.RevokedAt,
		RevokedReason: model.RevokedReason,
	}
}

// isNoWaitLockError 判断 MySQL 3572：NOWAIT 未能立即取得行锁。
func isNoWaitLockError(err error) bool {
	var mysqlErr *mysql.MySQLError
	return errors.As(err, &mysqlErr) && mysqlErr.Number == 3572
}
