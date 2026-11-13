// models.go 集中登记参与 AutoMigrate 的表模型，API 与 Worker 两个组合根共享同一份清单。
package migration

import (
	infraaccount "pulsefeed/internal/infra/persistence/account"
	infraclip "pulsefeed/internal/infra/persistence/clip"
	infraembedding "pulsefeed/internal/infra/persistence/embedding"
	infraimpression "pulsefeed/internal/infra/persistence/impression"
	infraaction "pulsefeed/internal/infra/persistence/interaction"
	inframessage "pulsefeed/internal/infra/persistence/message"
	infraoutbox "pulsefeed/internal/infra/persistence/outbox"
	infraplayback "pulsefeed/internal/infra/persistence/playback"
	infrarelation "pulsefeed/internal/infra/persistence/relation"
)

// Models 返回当前阶段全部表模型；后续阶段新增表时只在此处登记。
func Models() []any {
	return []any{
		&infraaccount.UserModel{},
		&infraaccount.UserProfileModel{},
		// 刷新令牌会话链：登录建链、轮换留同链、登出/重放按链吊销。
		&infraaccount.RefreshTokenModel{},
		&infraclip.ClipModel{},
		&infraclip.ClipStatsModel{},
		&infraaction.ActionModel{},
		&infraaction.CommentModel{},
		&infrarelation.FollowModel{},
		&infrarelation.UserStatsModel{},
		&infraimpression.PlayEventModel{},
		&infraimpression.ImpressionModel{},
		&infraembedding.ClipEmbeddingModel{},
		// 事务性 outbox：与业务写入同事务落库，由 Worker 中的 relay 负责投递。
		&infraoutbox.EventModel{},
		// S16 新增：消息、播放配置与 QoS 两张表。
		&inframessage.MessageModel{},
		&infraplayback.ConfigModel{},
		&infraplayback.QoSReportModel{},
	}
}
