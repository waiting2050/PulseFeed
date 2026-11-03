// migration.go 提供可重试的 AutoMigrate：并发建表产生的重复列/索引错误按 100ms 退避重试，最多 3 次。
package migration

import (
	"errors"
	"time"

	infrafeed "pulsefeed/internal/infra/persistence/feed"

	"github.com/go-sql-driver/mysql"
	"gorm.io/gorm"
)

const (
	// maxAttempts 为最大迁移尝试次数（含首次）。
	maxAttempts = 3
	// retryBackoff 为相邻两次重试之间的固定退避间隔。
	retryBackoff = 100 * time.Millisecond
)

// AutoMigrate 对给定模型切片执行 GORM 迁移；模型为空时不产生任何 DDL。
// 仅对并发迁移场景下的重复列（1060）/重复索引（1061）错误重试，其余错误直接返回。
// 建表完成后在迁移末尾补建 timeline 索引，索引已存在时跳过。
func AutoMigrate(db *gorm.DB, models ...any) error {
	var err error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if err = db.AutoMigrate(models...); err == nil || !isRetryableSchemaError(err) {
			break
		}
		time.Sleep(retryBackoff)
	}
	if err != nil {
		return err
	}
	return infrafeed.EnsureTimelineIndex(db)
}

// isRetryableSchemaError 判断错误是否为可重试的重复列/重复索引 MySQL 错误。
func isRetryableSchemaError(err error) bool {
	var mysqlErr *mysql.MySQLError
	if !errors.As(err, &mysqlErr) {
		return false
	}
	// 1060: ER_DUP_FIELDNAME；1061: ER_DUP_KEYNAME。
	return mysqlErr.Number == 1060 || mysqlErr.Number == 1061
}
