// service.go 编排 message 应用用例：按事件创建站内消息（幂等）、分页、未读计数与全部标记已读。
// 本服务同时实现 relation 域的 FollowEventMessenger 端口，供关注迁移时自动投递消息。
package appmessage

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"

	dommessage "pulsefeed/internal/domain/message"
)

// Option 用于装配可选依赖。
type Option func(*Service)

// Service 提供站内消息应用用例；仓储缺失时接口返回 ErrInvalidReceiver。
type Service struct {
	repo dommessage.Repository
}

// New 创建消息应用服务。
func New(repo dommessage.Repository, opts ...Option) *Service {
	s := &Service{repo: repo}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// CreateFromActorEvent 按显式 event_id 创建一条消息；仓储 uk_event_id 保证幂等，
// 调用方可通过 returned inserted 判断本次是否真正新增。
func (s *Service) CreateFromActorEvent(ctx context.Context, receiverID, actorID, targetID int64, kind dommessage.Kind, eventID string) (inserted bool, err error) {
	if receiverID <= 0 || actorID <= 0 {
		return false, dommessage.ErrInvalidReceiver
	}
	if eventID == "" {
		return false, dommessage.ErrEventIDEmpty
	}
	if !kindAllowed(kind) {
		return false, dommessage.ErrInvalidKind
	}
	msg := &dommessage.Message{
		ReceiverID: receiverID,
		ActorID:    actorID,
		Kind:       kind,
		TargetID:   targetID,
		EventID:    eventID,
	}
	return s.repo.Create(ctx, msg)
}

// ListResult 是消息分页结果。
type ListResult struct {
	Items      []*dommessage.Message
	NextCursor string
	HasMore    bool
}

// ListPage 分页接收者消息：cursor 为空取首页；limit 0 取默认、超 100 或负数返回 ErrInvalidLimit。
func (s *Service) ListPage(ctx context.Context, receiverID int64, cursor string, limit int) (*ListResult, error) {
	if receiverID <= 0 {
		return nil, dommessage.ErrInvalidReceiver
	}
	if limit < 0 || limit > dommessage.MaxLimit {
		return nil, dommessage.ErrInvalidLimit
	}
	size := limit
	if size == 0 {
		size = dommessage.DefaultLimit
	}
	c, err := dommessage.DecodeListCursor(cursor)
	if err != nil {
		return nil, err
	}
	rows, err := s.repo.ListPage(ctx, receiverID, c, size+1)
	if err != nil {
		return nil, err
	}
	result := &ListResult{Items: rows}
	if len(rows) > size {
		result.HasMore = true
		result.Items = rows[:size]
		last := result.Items[len(result.Items)-1]
		result.NextCursor = dommessage.EncodeListCursor(&dommessage.ListCursor{CreatedAt: last.CreatedAt, ID: last.ID})
	}
	return result, nil
}

// CountUnread 返回接收者未读数。
func (s *Service) CountUnread(ctx context.Context, receiverID int64) (int64, error) {
	if receiverID <= 0 {
		return 0, dommessage.ErrInvalidReceiver
	}
	return s.repo.CountUnread(ctx, receiverID)
}

// MarkAllRead 把接收者所有未读消息一次性标记已读。
func (s *Service) MarkAllRead(ctx context.Context, receiverID int64) error {
	if receiverID <= 0 {
		return dommessage.ErrInvalidReceiver
	}
	return s.repo.MarkAllRead(ctx, receiverID)
}

// --- FollowEventMessenger 端口实现：按确定性 event_id 投递关注通知 ---

// NotifyFollowed 向被关注者（targetUserID）发送关注通知；内部基于
// (receiver, actor, kind) 生成确定性 event_id，保证同一对用户重复事件幂等。
func (s *Service) NotifyFollowed(ctx context.Context, followerID, targetUserID int64) error {
	eventID := followEventID(targetUserID, followerID)
	_, err := s.CreateFromActorEvent(ctx, targetUserID, followerID, targetUserID, dommessage.KindFollowed, eventID)
	return err
}

// followEventID 为一次关注事件生成确定性 event_id（sha1 前 24 位 hex）。
func followEventID(receiverID, actorID int64) string {
	sum := sha1.Sum([]byte(fmt.Sprintf("%s|%d|%d", dommessage.KindFollowed, receiverID, actorID)))
	return hex.EncodeToString(sum[:])[:24]
}

// kindAllowed 校验消息种类是否在已知枚举内。
func kindAllowed(k dommessage.Kind) bool {
	switch k {
	case dommessage.KindFollowed:
		return true
	}
	return false
}
