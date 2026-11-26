// service.go 编排 message 应用用例：按事件创建站内消息（幂等）、分页、未读计数、按条/全部标记已读。
// 本服务同时实现 relation 与 interaction 的消息端口，供关注、点赞、评论事件自动投递通知。
package appmessage

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"strings"

	dommessage "pulsefeed/internal/domain/message"
)

// maxTitleRunes 与 pf_messages.title 的列宽保持一致。
const maxTitleRunes = 255

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

// Publish 按显式 event_id、标题与正文创建一条消息；仓储 uk_event_id 保证幂等。
// 标题为空时按消息种类补默认标题，正文允许为空；系统消息可没有发送者（actorID=0）。
func (s *Service) Publish(ctx context.Context, receiverID, actorID, targetID int64, kind dommessage.Kind, title, content, eventID string) (bool, error) {
	if receiverID <= 0 {
		return false, dommessage.ErrInvalidReceiver
	}
	kind = dommessage.NormalizeKind(kind)
	if !kindAllowed(kind) {
		return false, dommessage.ErrInvalidKind
	}
	if kind != dommessage.KindSystem && actorID <= 0 {
		return false, dommessage.ErrInvalidReceiver
	}
	if eventID == "" {
		return false, dommessage.ErrEventIDEmpty
	}
	title = strings.TrimSpace(title)
	if title == "" {
		title = dommessage.DefaultTitle(kind)
	}
	if len([]rune(title)) > maxTitleRunes {
		return false, dommessage.ErrInvalidTitle
	}
	msg := &dommessage.Message{
		ReceiverID: receiverID,
		ActorID:    actorID,
		Kind:       kind,
		TargetID:   targetID,
		Title:      title,
		Content:    strings.TrimSpace(content),
		EventID:    eventID,
	}
	return s.repo.Create(ctx, msg)
}

// CreateFromActorEvent 按事件创建一条消息，标题按种类兜底、正文为空；保留原有的调用契约。
func (s *Service) CreateFromActorEvent(ctx context.Context, receiverID, actorID, targetID int64, kind dommessage.Kind, eventID string) (bool, error) {
	return s.Publish(ctx, receiverID, actorID, targetID, kind, "", "", eventID)
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

// MarkReadByIDs 按 ID 列表把接收者名下的未读消息标记已读，返回实际更新行数。
// 空列表或含非正 ID 返回 ErrInvalidMessageIDs；越权 ID 由仓储按 receiver 过滤。
func (s *Service) MarkReadByIDs(ctx context.Context, receiverID int64, ids []int64) (int64, error) {
	if receiverID <= 0 {
		return 0, dommessage.ErrInvalidReceiver
	}
	if len(ids) == 0 {
		return 0, dommessage.ErrInvalidMessageIDs
	}
	for _, id := range ids {
		if id <= 0 {
			return 0, dommessage.ErrInvalidMessageIDs
		}
	}
	return s.repo.MarkReadByIDs(ctx, receiverID, ids)
}

// --- 关注事件端口实现 ---

// NotifyFollowed 向被关注者发送关注通知；确定性 event_id 保证同一对用户重复事件幂等。
func (s *Service) NotifyFollowed(ctx context.Context, followerID, targetUserID int64) error {
	if followerID == targetUserID {
		return nil
	}
	_, err := s.Publish(ctx, targetUserID, followerID, targetUserID, dommessage.KindFollow,
		"新的关注者", "关注了你", eventIDFor(dommessage.KindFollow, targetUserID, followerID, 0))
	return err
}

// --- 互动事件端口实现 ---

// NotifyLiked 向作品创作者发送点赞通知；同一读者对同一作品只投递一次。
func (s *Service) NotifyLiked(ctx context.Context, actorID, clipID, creatorID int64) error {
	if actorID == creatorID {
		return nil
	}
	_, err := s.Publish(ctx, creatorID, actorID, clipID, dommessage.KindLike,
		"收到新的点赞", "赞了你的作品", eventIDFor(dommessage.KindLike, creatorID, actorID, clipID))
	return err
}

// NotifyCommented 向作品创作者发送评论通知；event_id 基于评论 ID，重复提交幂等。
func (s *Service) NotifyCommented(ctx context.Context, actorID, clipID, commentID, creatorID int64, content string) error {
	if actorID == creatorID {
		return nil
	}
	_, err := s.Publish(ctx, creatorID, actorID, clipID, dommessage.KindComment,
		"收到新的评论", content, eventIDFor(dommessage.KindComment, creatorID, 0, commentID))
	return err
}

// eventIDFor 为事件生成确定性 event_id（sha1 前 24 位 hex），保证重复事件不新增消息。
func eventIDFor(kind dommessage.Kind, receiverID, actorID, targetID int64) string {
	sum := sha1.Sum([]byte(fmt.Sprintf("%s|%d|%d|%d", kind, receiverID, actorID, targetID)))
	return hex.EncodeToString(sum[:])[:24]
}

// kindAllowed 校验消息种类是否在已知枚举内。
func kindAllowed(k dommessage.Kind) bool {
	switch k {
	case dommessage.KindLike, dommessage.KindComment, dommessage.KindFollow, dommessage.KindSystem:
		return true
	}
	return false
}
