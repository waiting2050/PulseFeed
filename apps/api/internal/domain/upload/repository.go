// repository.go 定义上传领域仓储端口；所有状态迁移均以条件更新（CAS）表达，
// 返回值表示本次条件更新是否真正影响了一行。
package domupload

import (
	"context"
	"time"
)

// Repository 描述上传会话与分片元数据的持久化能力。
type Repository interface {
	// CreateSession 新建一条 pending 会话。
	CreateSession(ctx context.Context, session *Session) error
	// GetSession 按 upload_id 读取会话；不存在返回 ErrSessionNotFound。
	GetSession(ctx context.Context, uploadID string) (*Session, error)
	// FindReusable 查找可复用的 pending 会话：同用户 + 同文件名 + 同大小，
	// 且 updated_at 不早于 updatedAfter（避免复用已到期会话）；未命中返回 ErrSessionNotFound。
	FindReusable(ctx context.Context, userID int64, filename string, sizeBytes int64, updatedAfter time.Time) (*Session, error)
	// CountActive 统计该用户处于 pending / merging 的会话数。
	CountActive(ctx context.Context, userID int64) (int, error)
	// ListStale 返回过期的 pending 会话与卡住的 merging 会话，供兜底清扫使用。
	ListStale(ctx context.Context, pendingBefore, mergingBefore time.Time) ([]Session, error)
	// ExpireIfStale 条件置过期：仅当仍为 pending/merging 且 updated_at 早于 staleBefore。
	ExpireIfStale(ctx context.Context, uploadID string, staleBefore time.Time) (bool, error)
	// FailIfMergingStale 条件置失败：仅当仍为 merging 且 updated_at 早于 staleBefore。
	FailIfMergingStale(ctx context.Context, uploadID string, staleBefore time.Time) (bool, error)
	// MarkMerging 条件迁移 pending → merging。
	MarkMerging(ctx context.Context, uploadID string) (bool, error)
	// MarkPending 条件回滚 merging → pending，用于分片不齐时让客户端补传。
	MarkPending(ctx context.Context, uploadID string) (bool, error)
	// MarkCompleted 条件迁移 merging → completed 并写入结果字段。
	MarkCompleted(ctx context.Context, uploadID, url string, durationMS int64) (bool, error)
	// RevertCompletedToFailed 条件回滚 completed → failed，用于落位失败时撤销已完成标记。
	RevertCompletedToFailed(ctx context.Context, uploadID, reason string) (bool, error)
	// MarkFailed 条件迁移 pending/merging → failed 并记录原因。
	MarkFailed(ctx context.Context, uploadID, reason string) (bool, error)
	// MarkCanceled 条件迁移 pending/merging → canceled。
	MarkCanceled(ctx context.Context, uploadID string) (bool, error)
	// UpdateSHA256IfEmpty 仅在会话 sha256 为空时回填客户端期望值。
	UpdateSHA256IfEmpty(ctx context.Context, uploadID, sha256 string) error
	// UpdateCoverURL 写入抽帧得到的封面地址；失败只记录日志，不影响主流程。
	UpdateCoverURL(ctx context.Context, uploadID, coverURL string) error
	// UpsertPart 幂等写入分片元数据，并返回当前会话已收分片数。
	UpsertPart(ctx context.Context, uploadID string, partNumber int, sizeBytes int64) (int, error)
	// ListPartNumbers 按分片号升序返回已收分片号。
	ListPartNumbers(ctx context.Context, uploadID string) ([]int, error)
	// ActiveUploadIDs 返回仍处于 pending/merging 的会话 ID 集合。
	ActiveUploadIDs(ctx context.Context) (map[string]struct{}, error)
	// DeleteTerminalBefore 删除早于 before 的终态会话行与其分片行。
	DeleteTerminalBefore(ctx context.Context, before time.Time) error
}
