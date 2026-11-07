// database.go 负责创建 MySQL 连接池，并在返回前用带超时的 Ping 验证连通性。
package infradatabase

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	infraconfig "pulsefeed/internal/infra/config"

	_ "github.com/go-sql-driver/mysql"
)

// 连接池参数常量：小项目使用固定值，后续按压测结果调整。
const (
	maxOpenConns    = 50
	maxIdleConns    = 10
	connMaxLifetime = 30 * time.Minute

	// dialTimeout 限制 TCP 拨号时长，pingTimeout 限制首次连通性验证时长；
	// 二者保证 MySQL 不可用时启动在 30s 内失败退出，不空转。
	dialTimeout = 5 * time.Second
	pingTimeout = 10 * time.Second
)

// New 按配置创建 MySQL 连接池；失败时错误携带 host/port/db 便于定位。
func New(dbcfg infraconfig.DatabaseConfig) (*sql.DB, error) {
	// parseTime=true 让 DATETIME/TIMESTAMP 自动映射为 time.Time。
	dsn := fmt.Sprintf(
		"%s:%s@tcp(%s:%d)/%s?charset=utf8mb4&parseTime=true&loc=Local&timeout=%s",
		dbcfg.User,
		dbcfg.Password,
		dbcfg.Host,
		dbcfg.Port,
		dbcfg.Name,
		dialTimeout,
	)

	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, fmt.Errorf("打开 MySQL 连接 %s:%d/%s 失败: %w", dbcfg.Host, dbcfg.Port, dbcfg.Name, err)
	}

	db.SetMaxOpenConns(maxOpenConns)
	db.SetMaxIdleConns(maxIdleConns)
	db.SetConnMaxLifetime(connMaxLifetime)

	ctx, cancel := context.WithTimeout(context.Background(), pingTimeout)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("连接 MySQL %s:%d/%s 失败: %w", dbcfg.Host, dbcfg.Port, dbcfg.Name, err)
	}

	return db, nil
}
