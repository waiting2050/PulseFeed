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
	// ErrAccountDisabled 表示账号状态非正常（如被禁用），不允许登录。
	ErrAccountDisabled = errors.New("账号已被禁用或状态异常")
	// ErrSessionInvalid 表示刷新令牌无效、已吊销、已过期或判定为重放，需要重新登录。
	ErrSessionInvalid = errors.New("登录状态已失效，请重新登录")
	// ErrRefreshLimited 表示刷新请求触发限流，需要退避后重试。
	ErrRefreshLimited = errors.New("请求过于频繁，请稍后重试")
	// ErrLoginLimited 表示登录请求触发限流（按 IP 或账号），需要退避后重试。
	ErrLoginLimited = errors.New("登录请求过于频繁，请稍后重试")
)
