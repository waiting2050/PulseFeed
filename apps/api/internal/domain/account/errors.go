// errors.go 集中定义 account 领域错误哨兵，传输层据此映射 HTTP 状态码。
package domainaccount

import "errors"

var (
	// ErrAccountExists 表示账号名已被注册（唯一索引冲突）。
	ErrAccountExists = errors.New("account already exists")
	// ErrInvalidCredentials 表示登录凭证校验失败。
	ErrInvalidCredentials = errors.New("invalid credentials")
	// ErrInvalidNickname 表示昵称为空或超长（64 个字符）。
	ErrInvalidNickname = errors.New("昵称不能为空且不能超过 64 个字符")
	// ErrInvalidAccountName 表示账号名为空或长度越界。
	ErrInvalidAccountName = errors.New("invalid account name")
	// ErrAccountNotFound 表示账号或资料不存在。
	ErrAccountNotFound = errors.New("account not found")
	// ErrEmptyProfileUpdate 表示资料更新请求不包含任何字段。
	ErrEmptyProfileUpdate = errors.New("profile update is empty")
)
