# PulseFeed 路由与端口快照

> 生成时间：2026-11-02；2026-11-06 按 `router.go` 与 `docker-compose.yml` 复核字段、字符上限与可配置端口；2026-11-07 终检：按代码复核 QoS 字段与 SPA 深链回落，补充死信拓扑索引。
> 依据 `apps/api/internal/interfaces/http/router/router.go` 实际注册路由与 `apps/docker-compose.yml`。
> 所有接口统一返回 `{code, message, data}`，成功 `code=0`；请求体与响应体均为 JSON。

## 1. 鉴权约定

- **公开**：无需 token。
- **可选**：携带合法 `Authorization: Bearer <token>` 时回显读者互动状态，不携带也能读取。
- **登录**：必须携带 `Authorization: Bearer <token>`，缺失/失效返回 401。
- **内部**：必须携带 `X-Internal-Token: <internal.token>`。
- 前端写请求（非 GET/HEAD）统一携带 `Idempotency-Key`：发布要求非空，同 key 返回同一 clip；评论允许缺省，同 key 重放返回同一评论；点赞/收藏/关注由唯一行天然幂等。互动、关注、评论接口对去空白后超 128 字符的 key 返回 400。
- 错误响应同一信封：`{code, message, data}`，`code!=0` 且 HTTP 状态码对应语义（400 参数、401 未登录、403 非本人、404 不存在、409 冲突）。

## 2. HTTP 路由清单

### 2.1 账号与用户

| 方法 | 路径 | 鉴权 | 请求要点 | 响应 data 要点 |
|---|---|---|---|---|
| POST | `/api/users` | 公开 | `{account, password, nickname}` | `{user_id, account, nickname, avatar_url, bio}` |
| POST | `/api/sessions` | 公开 | `{account, password}` | `{access_token, token_type, expires_in, user_id, account}` |
| DELETE | `/api/sessions/current` | 登录 | 空 | `null` |
| GET | `/api/users/me` | 登录 | — | `{user_id, nickname, avatar_url, bio}` |
| PATCH | `/api/users/me` | 登录 | `{nickname?, avatar_url?, bio?}` | 更新后的用户资料 |
| GET | `/api/users/:userId` | 公开 | — | `{user_id, account?, nickname, avatar_url, bio}` |
| GET | `/api/users/:userId/clips` | 公开 | `?cursor=&limit=` | `{items:[{clip_id,creator_id,title,video_url,cover_url,duration_ms,published_at}], next_cursor, has_more}` |

### 2.2 Feed 与推荐读取

| 方法 | 路径 | 鉴权 | 请求要点 | 响应 data 要点 |
|---|---|---|---|---|
| GET | `/api/feed` | 可选 | `?scene=latest\|following\|for-you\|hot&cursor=&limit=` | `{items:[{clip_id,creator_id,title,video_url,cover_url,duration_ms,published_at,like_count,save_count,comment_count,play_count,liked,saved}], next_cursor, has_more}` |
| POST | `/api/feed-search` | 可选 | `{scene, cursor, limit}` | 同上（与 GET 同义） |

> 推荐/展示（impression）类内部接口挂载在 `/internal` 前缀，见 2.6。

### 2.3 发布与上传

| 方法 | 路径 | 鉴权 | 请求要点 | 响应 data 要点 |
|---|---|---|---|---|
| POST | `/api/clips` | 登录 | `{title, video_url, cover_url, duration_ms?}` | `{clip_id, creator_id, title, video_url, cover_url, duration_ms, published_at}` |
| DELETE | `/api/clips/:clipId` | 登录 | `Idempotency-Key` | `null` |
| GET | `/api/clips/:clipId` | 公开 | — | `{clip_id, creator_id, title, video_url, cover_url, duration_ms, published_at}` |
| GET | `/api/users/me/clips` | 登录 | `?cursor=&limit=` | `{items, next_cursor, has_more}` |
| POST | `/api/uploads` | 登录 | `multipart/form-data: file, kind` | `{url, kind, cover_url?, duration_ms?}`（静态回读 `/uploads/<name>`） |

> `duration_ms` 为 0 或缺省表示未探测到时长；`cover_url` 缺省表示未生成首帧，前端使用占位封面。
> 上传媒体文件时会调用 ffprobe/ffmpeg：工具缺失只记录 warning，接口仍返回 201，不影响发布。
> 标题按字符（rune）计，上限 128；昵称上限 64；评论上限 1000。均先去首尾空白再判定，与列宽一致。

### 2.4 互动（点赞/收藏/评论）

| 方法 | 路径 | 鉴权 | 请求要点 | 响应 data 要点 |
|---|---|---|---|---|
| PUT | `/api/clips/:clipId/like` | 登录 | 空 | `{clip_id, action_type, active, like_count}` |
| DELETE | `/api/clips/:clipId/like` | 登录 | 空 | 同上 |
| PUT | `/api/clips/:clipId/save` | 登录 | 空 | `{clip_id, action_type, active, save_count}` |
| DELETE | `/api/clips/:clipId/save` | 登录 | 空 | 同上 |
| POST | `/api/clips/:clipId/comments` | 登录 | `{content}` | `{comment_count, comment:{id,clip_id,user_id,user_nickname,user_avatar_url,content,created_at}}` |
| DELETE | `/api/comments/:commentId` | 登录 | — | `{comment_id, status, comment_count}` |
| GET | `/api/clips/:clipId/comments` | 公开 | `?cursor=&limit=` | `{items:[{id,clip_id,user_id,user_nickname,user_avatar_url,content,created_at}], next_cursor, has_more}` |

### 2.5 播放上报、关注、消息、播放配置

| 方法 | 路径 | 鉴权 | 请求要点 | 响应 data 要点 |
|---|---|---|---|---|
| POST | `/api/clip-play-events` | 登录 | `{event_id, clip_id, play_duration_ms, completed, impression}` | `{event_id}` |
| PUT | `/api/users/me/following/:targetUserId` | 登录 | 空 | `{user_id, target_user_id, status, following, following_count, follower_count}` |
| DELETE | `/api/users/me/following/:targetUserId` | 登录 | 空 | 同上 |
| GET | `/api/users/me/following` | 登录 | `?cursor=&limit=` | `{items:[{user_id,nickname,avatar_url,bio,followed_at}], next_cursor, has_more}` |
| GET | `/api/users/me/followers` | 登录 | `?cursor=&limit=` | 同上 |
| GET | `/api/messages` | 登录 | `?cursor=&limit=` | `{items:[{id,actor_id,kind,target_id,event_id,read,read_at,created_at}], next_cursor, has_more}` |
| GET | `/api/message-stats/unread` | 登录 | — | `{unread}` |
| PATCH | `/api/messages` | 登录 | 空 | 全部标记已读后的未读数 |
| GET | `/api/playback-config` | 登录 | — | `{preload_count, qos_sample_rate}` |
| PUT | `/api/playback-config` | 登录 | `{preload_count?, qos_sample_rate?}` | 更新后的配置 |
| GET | `/api/preload-clips` | 登录 | — | `{clip_ids:[int64]}` |
| POST | `/api/playback-qos-reports` | 登录 | `{clip_id, event_id, latency_ms, stall_count, completed, client_info?}` | `{inserted}` |

> QoS 以 `event_id` 唯一键幂等：重复上报返回 `inserted=false`，不重复聚合；`client_info` 为可选字符串，前端放入 `{"duration_ms":<实际播放时长>}`。
> 消息队列的死信交换机、重试上限与 406 旧队列处理不属于 HTTP 契约，见 `docs/architecture.md` 2.2.1。

### 2.6 内部接口（`X-Internal-Token`）

| 方法 | 路径 | 请求要点 | 响应 data 要点 |
|---|---|---|---|
| POST | `/internal/recommendation-candidates` | `{viewer_id, limit}` | 候选列表 |
| GET | `/internal/impression-decisions` | `?viewer_id=&clip_id=` | 展示决策 |
| GET | `/internal/impressions` | `?viewer_id=&days=` | 展示聚合 |
| POST | `/internal/messages` | `{receiver_id, actor_id, target_id, kind, event_id}` | 站内信幂等创建 |
| POST | `/internal/playback-qos-reports` | 同 QoS 上报 | `{inserted}` |

## 3. 容器端口与网关转发

宿主端口均可由 `apps/.env` 覆盖（模板 `apps/.env.example`）；下表为默认映射，容器内端口固定。

| 服务 | 默认宿主端口 | 覆盖变量 | 容器 | 说明 |
|---|---|---|---|---|
| web | `8088` | `WEB_PORT` | `80` | 前端入口；nginx 将 `/api/`、`/uploads/` 原样转发到 `http://api:8090` |
| api | `8090` | `API_PORT` | `8090` | `/health`、`/metrics` 与全部业务路由 |
| worker | `19092` | `WORKER_METRICS_PORT` | `9092` | 默认值避开 Win11 Hyper-V 保留段 9020–9119 |
| mysql | `3307` | `MYSQL_PORT` | `3306` | 避免与本机 3306 冲突 |
| redis | `6380` | `REDIS_PORT` | `6379` | |
| rabbitmq | `5673` / `15673` | `RABBITMQ_PORT` / `RABBITMQ_UI_PORT` | `5672` / `15672` | 15673 为管理台 |
| prometheus | `19090` | `PROMETHEUS_PORT` | `9090` | 仅 `--profile monitoring`；9090 落在保留段，故默认映射 19090 |
| grafana | `3000` | `GRAFANA_PORT` | `3000` | 仅 `--profile monitoring` |

前端开发态（`npm run dev`）与容器态路径一致：均以 `/api`、`/uploads` 为前缀，无需区分环境改 baseURL。

前端页面路由（`/login`、`/feed`、`/publish`、`/messages`、`/playback`、`/clip/:clipId`）由 React Router 在浏览器内处理；容器内 nginx 以 `try_files $uri $uri/ /index.html` 回落到 `index.html`，刷新与直接粘贴深链不会 404。
