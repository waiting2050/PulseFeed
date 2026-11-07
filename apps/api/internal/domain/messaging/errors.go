// errors.go 定义事件消费错误的可重试性标记：永久性错误重试不会成功，由 MQ 转入死信队列。
package messaging

import "errors"

// permanentError 包装不可重试的消费错误。
type permanentError struct {
	err error
}

// Error 返回底层错误描述。
func (e *permanentError) Error() string { return e.err.Error() }

// Unwrap 保留底层错误，供 errors.Is/As 继续判定领域错误。
func (e *permanentError) Unwrap() error { return e.err }

// Permanent 把错误标记为不可重试；nil 保持 nil，避免把成功误判为失败。
func Permanent(err error) error {
	if err == nil {
		return nil
	}
	return &permanentError{err: err}
}

// IsPermanent 判断错误是否被标记为不可重试。
func IsPermanent(err error) bool {
	var target *permanentError
	return errors.As(err, &target)
}
