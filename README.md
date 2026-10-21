# PulseFeed

PulseFeed 是一个短视频 Feed 系统，后端采用 Go 单体架构，按业务垂直切片组织代码。当前已完成账户鉴权、视频上传与发布能力，Feed 分发、互动、推荐等模块按里程碑继续推进。

## 技术栈

- Go 1.26、Gin
- GORM、MySQL 8
- Redis 7
- JWT
- Docker Compose

## 当前进度

- [x] 工程骨架：配置、日志、MySQL/Redis 连接、优雅退出
- [x] 账户模块：注册、登录、JWT、个人资料
- [x] 视频模块：文件上传、发布、详情、幂等、作者作品列表
- [x] Feed 模块：Timeline 游标分页、Redis 页缓存、统计字段组装
- [x] Hot Feed：Redis 分钟桶 ZSET、窗口合并
- [x] 互动模块：点赞、收藏、评论、计数与热榜联动
- [ ] 互动异步落库：RabbitMQ 削峰
- [x] 关系模块：关注、取关、关注流
- [x] 推荐与曝光：多路召回、曝光去重、作者打散
- [ ] 消息通知
- [ ] 监控与治理
- [ ] Web 客户端

## 目录结构

```text
cmd/api/                    服务启动与依赖装配
internal/
  shared/                   配置、日志、数据库、Redis、JWT、响应、中间件、游标
  account/                  账户垂直切片
    domain/                 实体、领域错误、仓储接口
    app/                    注册、登录、资料更新用例
    infra/                  GORM 仓储
    http/                   路由、DTO、Handler
  video/                    视频垂直切片
    domain/                 视频实体、统计实体、仓储接口
    app/                    发布、详情、作品列表用例
    infra/                  GORM 仓储
    http/                   路由、DTO、Handler
  feed/                     Feed 垂直切片
    domain/                 Feed 卡片实体、分页结果、仓储与缓存接口
    app/                    Timeline / Hot 查询、游标编解码、页缓存读写
    infra/                  GORM 回源查询、Redis 页缓存
    http/                   路由、DTO、Handler
  interaction/              互动垂直切片
    domain/                 行为、评论实体与仓储接口
    app/                    点赞、收藏、评论用例
    infra/                  GORM 仓储、计数事务
    http/                   路由、DTO、Handler
  relation/                 关系垂直切片
    domain/                 关注关系实体与仓储接口
    app/                    关注、取关用例
    infra/                  GORM 仓储
    http/                   路由、DTO、Handler
  recommend/                推荐垂直切片
    domain/                 召回源、曝光存储接口
    app/                    多路召回合并、曝光去重、作者打散
    infra/                  热榜/关注/时间线召回、Redis 曝光集合
    http/                   路由、DTO、Handler
  upload/                   文件上传
  httpapi/                  Gin 引擎、健康检查、静态文件
```

每个业务切片内部保持 `domain -> app -> infra/http` 的依赖方向，切片之间不直接依赖对方的 infra 实现。

## 快速启动

### Docker Compose

```bash
docker compose up -d --build
```

服务地址：

| 服务 | 地址 |
| --- | --- |
| API | `http://127.0.0.1:8080` |
| 健康检查 | `http://127.0.0.1:8080/health` |
| MySQL | `127.0.0.1:3307` |
| Redis | `127.0.0.1:6379` |

停止并清理数据：

```bash
docker compose down -v
```

### 本地运行

需要本地已有 MySQL 和 Redis，并创建 `pulsefeed` 数据库。复制 `.env.example` 为 `.env` 后按需修改，然后启动：

```bash
go run ./cmd/api
```

## API

统一响应结构：

```json
{
  "code": 0,
  "message": "ok",
  "data": {}
}
```

已实现接口：

| 方法 | 路径 | 鉴权 | 功能 |
| --- | --- | --- | --- |
| POST | `/api/users` | 否 | 注册 |
| POST | `/api/sessions` | 否 | 登录并获取 JWT |
| DELETE | `/api/sessions/current` | 是 | 退出登录 |
| GET | `/api/users/me` | 是 | 当前用户资料 |
| PATCH | `/api/users/me` | 是 | 更新昵称、头像、简介 |
| GET | `/api/profiles/{userId}` | 否 | 公开用户资料 |
| POST | `/api/uploads` | 是 | 上传视频、封面或头像 |
| POST | `/api/videos` | 是 | 发布视频，支持 `Idempotency-Key` |
| GET | `/api/videos/{videoId}` | 否 | 视频详情 |
| GET | `/api/users/me/videos` | 是 | 我的作品列表 |
| GET | `/api/profiles/{userId}/videos` | 否 | 指定用户的作品列表 |
| GET | `/api/feed-items` | 否 | Timeline Feed，支持 `scene`、`cursor`、`limit` |
| POST | `/api/videos/{videoId}/likes` | 是 | 点赞 |
| DELETE | `/api/videos/{videoId}/likes` | 是 | 取消点赞 |
| POST | `/api/videos/{videoId}/favorites` | 是 | 收藏 |
| DELETE | `/api/videos/{videoId}/favorites` | 是 | 取消收藏 |
| POST | `/api/videos/{videoId}/comments` | 是 | 发表评论 |
| GET | `/api/videos/{videoId}/comments` | 否 | 评论列表，支持 `cursor`、`limit` |
| DELETE | `/api/comments/{commentId}` | 是 | 删除自己的评论 |
| POST | `/api/users/{userId}/follow` | 是 | 关注用户 |
| DELETE | `/api/users/{userId}/follow` | 是 | 取消关注 |
| GET | `/api/feed-items/following` | 是 | 关注流，支持 `cursor`、`limit` |
| GET | `/api/feed-items/recommend` | 是 | 推荐 Feed，多路召回与曝光去重 |

### 上传示例

```bash
curl -X POST http://127.0.0.1:8080/api/uploads \
  -H "Authorization: Bearer <token>" \
  -F "kind=video" \
  -F "file=@demo.mp4"
```

返回的 `url` 可直接用于发布视频的 `media_url`。

### 发布示例

```bash
curl -X POST http://127.0.0.1:8080/api/videos \
  -H "Authorization: Bearer <token>" \
  -H "Content-Type: application/json" \
  -H "Idempotency-Key: publish-demo-1" \
  -d '{"title":"第一条视频","media_url":"/uploads/video/demo.mp4"}'
```

相同的 `Idempotency-Key` 重复提交会返回同一条视频，不会重复创建。

## 设计说明

- **垂直切片**：每个业务模块自带 domain、app、infra、http，模块边界清晰，后续可独立拆分。
- **JWT 鉴权**：HS256 签名，中间件解析后把用户 ID 注入请求上下文。
- **发布幂等**：`Idempotency-Key` 先查后建，并由数据库唯一索引兜底并发写入。
- **游标分页**：作品列表与 Feed 都按 `published_at DESC, id DESC` 排序，游标编码最后一条的排序字段，翻页稳定。
- **Feed 页缓存**：一页 Feed 以 `feed:page:v1:{scene}:{limit}:{cursor}` 整体写入 Redis，TTL 60 秒；未命中时回源 MySQL 一次取出 `limit+1` 条判断 `has_more`，Redis 不可用时自动退化为直查数据库。
- **Hot Feed**：互动分数写入 `feed:hot:minute:v1:{yyyyMMddHHmm}` 分钟桶 ZSET，查询时把最近 60 个桶合并成临时 ZSET 取 TopN，游标编码 `(score, video_id)` 保证翻页稳定；Redis 未就绪时热榜返回空页。
- **互动幂等与计数**：`interaction_actions` 以 `(user_id, video_id, action)` 唯一索引兜底重复点赞与收藏；行为写入和 `video_stats` 计数更新在同一事务内完成，计数用单条 `UPDATE ... GREATEST(...)` 自增自减，避免并发覆盖和负数。
- **互动与热榜联动**：只有行为真正发生变化时才写入热榜分数，权重为 like=1、favorite=2、comment=3，取消操作写负分；热榜写入失败不阻断互动主流程。
- **关注流**：`user_relations` 以 `(follower_id, followee_id)` 唯一索引保证重复关注幂等；关注流先取当前用户的关注列表（最多 1000 人），再用 `author_id IN (...)` 回源视频并按发布时间游标翻页，不做整页缓存以反映关注关系的最新变化。
- **推荐流**：热榜、关注作者作品、时间线三路各召回 50 条，合并去重后过滤 Redis 中 `recommend:exposed:v1:{userID}` 已曝光的视频，再按“单页同一作者最多 2 条”打散并截断到 `limit`；本页结果写回曝光集合（TTL 24 小时），Redis 不可用时自动跳过曝光过滤。
- **上传校验**：按业务类型限制扩展名和文件大小，文件名由服务端生成，避免路径注入。
- **统一错误映射**：领域错误在 HTTP 层映射为对应状态码，内部错误只记录日志，不向外暴露细节。

## 验证

```bash
go build ./...
go test ./...
go vet ./...
docker compose config
```

## 后续路线

1. 互动异步化：RabbitMQ 削峰、点赞评论异步落库
2. 消息、监控治理与 Web 客户端
