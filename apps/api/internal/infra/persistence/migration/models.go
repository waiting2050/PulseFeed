// models.go 集中登记参与 AutoMigrate 的表模型，API 与 Worker 两个组合根共享同一份清单。
package migration

import (
	infraaccount "pulsefeed/internal/infra/persistence/account"
	infraclip "pulsefeed/internal/infra/persistence/clip"
	infraimpression "pulsefeed/internal/infra/persistence/impression"
	infraaction "pulsefeed/internal/infra/persistence/interaction"
	infrarelation "pulsefeed/internal/infra/persistence/relation"
)

// Models 返回当前阶段全部表模型；后续阶段新增表时只在此处登记。
func Models() []any {
	return []any{
		&infraaccount.UserModel{},
		&infraaccount.UserProfileModel{},
		&infraclip.ClipModel{},
		&infraclip.ClipStatsModel{},
		&infraaction.ActionModel{},
		&infraaction.CommentModel{},
		&infrarelation.FollowModel{},
		&infrarelation.UserStatsModel{},
		&infraimpression.PlayEventModel{},
		&infraimpression.ImpressionModel{},
	}
}
