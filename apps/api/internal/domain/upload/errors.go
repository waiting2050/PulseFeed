// errors.go 定义上传领域的哨兵错误。
package domupload

import "errors"

// ErrSessionNotFound 表示上传会话不存在。
var ErrSessionNotFound = errors.New("upload session not found")
