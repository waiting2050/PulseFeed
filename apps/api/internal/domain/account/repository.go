// repository.go 定义 account 领域的仓储端口，应用层只依赖该接口。
package domainaccount

import "context"

// Repository 描述账号与资料的持久化能力，由 infra 层实现。
type Repository interface {
	// Create 在同一事务内写入账号与资料；账号名重复时返回 ErrAccountExists，并回写自增 ID。
	Create(ctx context.Context, account *Account, profile *UserProfile) error
	// FindByAccount 按规范化账号名查找账号，登录流程使用；不存在时返回 ErrAccountNotFound。
	FindByAccount(ctx context.Context, name string) (*Account, error)
	// GetProfile 按用户 ID 读取资料；不存在时返回 ErrAccountNotFound。
	GetProfile(ctx context.Context, userID int64) (*UserProfile, error)
	// UpdateProfile 只更新资料展示字段；目标不存在时返回 ErrAccountNotFound。
	UpdateProfile(ctx context.Context, profile *UserProfile) error
}
