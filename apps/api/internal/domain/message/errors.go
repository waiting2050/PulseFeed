// errors.go 定义 message 领域的哨兵错误。
package dommessage

import "errors"

// ErrInvalidReceiver 表示消息接收者非法（<=0）。
var ErrInvalidReceiver = errors.New("invalid message receiver")

// ErrInvalidLimit 表示分页 limit 越界。
var ErrInvalidLimit = errors.New("invalid message limit")

// ErrInvalidTitle 表示标题为空或超过列宽上限。
var ErrInvalidTitle = errors.New("invalid message title")

// ErrInvalidKind 表示消息种类未知。
var ErrInvalidKind = errors.New("invalid message kind")

// ErrInvalidMessageIDs 表示按条已读传入的 ID 列表为空或包含非正整数。
var ErrInvalidMessageIDs = errors.New("invalid message ids")

// ErrEventIDEmpty 表示幂等事件 ID 为空，无法保障去重。
var ErrEventIDEmpty = errors.New("message event_id must not be empty")

// ErrCursorInvalid 表示游标解码失败或缺失必要字段。
var ErrCursorInvalid = errors.New("invalid message cursor")
