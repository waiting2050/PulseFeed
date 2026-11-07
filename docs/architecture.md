# PulseFeed 架构说明

本文只描述当前已实现的结构与数据流，不包含后续规划。

## 1. 分层边界

```
interfaces/http  →  application  →  domain
        ↑                 ↑            ↑
        └──────────── infra ───────────┘（实现 domain 端口，由组合根注入）
```

- `domain/`：实体、值对象、错误哨兵、仓储端口；不依赖框架与基础设施。
- `application/`：用例编排（账号、发布、Feed 策略、互动、关注、推荐、消息、播放）与 MQ Worker；只依赖 `domain` 端口和 `application/observability` 的指标端口。
- `infra/`：GORM 仓储、Redis 缓存、RabbitMQ 客户端、JWT、Prometheus 指标、配置加载；实现 `domain` 端口。
- `interfaces/http/`：DTO、Handler、JWT/内部 token 中间件、路由装配；统一响应 `{code,message,data}`。
- `cmd/api`、`cmd/worker`：两个组合根，负责配置校验、依赖装配、进程生命周期与优雅退出。

## 2. 关键数据流

### 2.1 发布与扇出

1. `POST /api/clips` 校验登录、标题、可选简介与媒体地址，按 `Idempotency-Key` 幂等写入 `pf_clips` 与 `pf_clip_stats`。简介为 `TEXT` 列，去首尾空白后按码点上限 1000 校验，超限返回 400 且不截断。
2. 发布成功后投递 `clip.published`（含 `clip_id`、`creator_id`、`published_at`）。
3. `FanoutWorker` 读取创作者粉丝数：达到大 V 阈值只写创作者 outbox（拉模型）；否则按 500/批写入粉丝 inbox（推模型），并预热卡片与计数缓存。
4. 关注类 Feed 优先读 inbox/outbox，未命中时回源 DB 并按游标分页。

### 2.2 互动

1. 点赞/收藏先以 `(user_id, clip_id, action_type)` 幂等更新 Redis 与 DB 状态，计数只在状态变化时增减。
2. 状态变化后投递 `interaction.action_changed`；`ActionWorker` 消费后维护计数与消息事件。
3. 评论走同步落库，按 `Idempotency-Key` 去重；评论数随创建/删除同步更新。
4. 前端对点赞/收藏做乐观更新，请求失败时回滚。
5. 站内消息分四类：点赞、评论、关注在状态真正生效时投递通知，系统消息只保留内部写入入口；自己操作自己的内容不产生消息，取消点赞或删除评论不撤回历史消息。消息带标题与正文，已读状态复用 `read_at` 派生布尔值，不新增 `is_read` 列；`PATCH /api/messages` 传 ID 列表时按条已读，不传 ID 时保持全部已读。

### 2.2.1 消费失败与死信分流

- **拓扑**：四组消费队列（`pulsefeed.clip.published`、`pulsefeed.clip.embedding`、`pulsefeed.interaction.action_changed`、`pulsefeed.impression.play_recorded`）均带 `x-dead-letter-exchange`；死信交换机为 `pulsefeed.<域>.dlx`，死信队列为 `<原队列>.dead`，路由键为 `dead.<原路由键>`，Worker 启动时与业务队列一并声明。
- **可重试失败**：失败先 `basic.nack(requeue=true)` 回原队列；同一消费进程按消息体哈希累计次数，上限 3 次（含首次）。达到上限后 `nack(requeue=false)`，由 broker 经死信交换机转入死信队列，主队列不再堆积该消息。
- **不可重试失败**：消息体 JSON 非法、必需字段缺失、目标 clip 不可见等确定性错误包装为 `messaging.Permanent`，直接 nack 进死信，不消耗重试次数。
- **计数口径**：失败次数只保存在消费进程内存中，Worker 重启后清零；跨重启累计需依赖 broker 的 `x-death` 头，不在本轮范围。
- **旧队列兼容**：队列已按旧参数（无死信参数）创建时，`QueueDeclare` 返回 `PRECONDITION_FAILED(406)`；Worker 直接报错退出并提示需要删除的队列名，需人工删除旧队列后重启，不静默改参。

### 2.3 展示（impression）、播放与 QoS

1. `POST /api/clip-play-events` 接受四类 `event_type`：`exposed`（卡片进入视口）、`play`（开始播放）、`complete`（播放到结尾）、`skip`（展示后未播放即划走）；空值与旧客户端语义一致，按 `play` 处理，其余未知值返回 400。`scene` 必须属于 Feed 场景或为空，`request_id` 上限 128 字符，`watch_ms` 取值 0–24 小时。
2. `event_id` 全局唯一，重复上报不二次写入 `pf_play_events`；`play_count` 只在 `event_type=play` 且首次插入时累加，并与 `pf_playback_qos_reports` 按同一 `event_id` 跨表去重。`exposed` / `skip` / `complete` 只 upsert 当日展示聚合，其中 `complete` 额外累加完播数，不影响 `play_count`。
3. 事件投递 `impression.play_recorded`；`PlayEventWorker`（D7 修正）消费后完成展示聚合补偿，避免“只发不收”。
4. `POST /api/playback-qos-reports` 按 `event_id` 幂等写入 QoS 表；仅首次插入参与采样与延迟/卡顿/完播聚合。
5. 播放配置按用户持久化 `preload_count`（默认 8，上限 32）、`qos_sample_rate`（默认 0.1，上限 1）、`buffer_ms`（默认 2000，夹取到 500–10000，越界按边界值落库）。`buffer_ms` 目前只有全局维度，平台/网络维度细分需要调整配置表主键，本轮未做。
6. `GET /api/preload-clips` 按用户 `preload_count` 返回最新 N 条可见内容的 `clip_id`、`media_url`、`cover_url`、`duration_ms`。前端在首屏数据就绪后调用该接口，以并发 3 的队列预取封面并读取媒体元信息；媒体元信息使用游离的媒体元素（`preload="metadata"`）拉取，5 秒超时兜底。场景切换或离开页面时通过 `AbortController` 统一取消，单个条目失败静默跳过，不阻塞渲染。
7. 前端播放器采集口径：会话开始（调用 `play()`）到首次 `playing` 记为 `latency_ms`（首帧等待）；播放中每次 `waiting` 计 1 次 `stall_count`；`client_info.duration_ms` 只累计实际播放墙钟时间（暂停与缓冲不计）；`ended` 或 `currentTime` 距媒体总时长 ≤0.5s 记为完播。
8. 每次播放会话生成一个 `event_id`（`crypto.randomUUID`，缺失时用时间戳随机串）；会话结算时按 `qos_sample_rate` 抽样一次，命中才上报，未命中不上报也不写上报标记；同一会话不重复随机。重复提交由后端 `event_id` 唯一键吸收，返回 `inserted=false`，不重复聚合。

### 2.4 Feed 读取

`GET /api/feed?scene=...` 由 `application/feed.Service` 按场景选择策略：

| 场景 | 排序 / 来源 | 游标 |
|---|---|---|
| latest | `published_at DESC, clip_id DESC` | 复合游标 `(published_at, clip_id)` |
| following | inbox/outbox，回源 DB | 复合游标 `(published_at, clip_id)` |
| hot | 热度分数（点赞/收藏/评论/播放加权） | 复合游标 `(score, clip_id)`，窗口为最近 60 个分钟桶 |
| for-you | 推荐候选 → 兴趣向量与热度加权 → 按创作者交错 | 携带 `snapshot_at`，交错后按最后实际输出条目生成 |

分页统一 `limit+1` 判断 `has_more`，响应返回 Base64 `next_cursor`；跨页按 `clip_id` 去重。for-you 的 `snapshot_at` 保证翻页期间候选池不随新发布漂移。

### 2.5 上传与媒体探测

1. `POST /api/uploads` 的 `kind` 取媒体 / 封面 / 头像三类，扩展名白名单分别为 `.mp4`/`.mov`/`.webm` 与 `.jpg`/`.jpeg`/`.png`/`.webp`。校验顺序固定为大小 → 扩展名 → 内容探测：超限文件不落盘、不探测。
2. 硬校验上限：媒体单文件 512MB、图片单文件 20MB、媒体时长 10 分钟、分辨率长边 3840（允许 2160×3840 竖屏 4K，按长边而非短边判定）。媒体编码白名单为 `h264`/`hevc`/`vp9`，其余编码返回 400。
3. 媒体内容探测由 `infra/media.Prober` 单次 `ffprobe` 完成（5 秒超时），一次读取时长、宽高与编码名，避免重复探测；时长无法识别、宽高缺失、超时长/超长边/编码不在白名单均删除已落盘文件并返回 400。
4. 图片内容校验：jpg/png 用标准库 `image.DecodeConfig` 校验真实格式与扩展名一致；webp 因标准库无解码器，只做容器级校验（RIFF/WEBP 头、声明长度不越界、至少包含 VP8/VP8L/VP8X 块），不校验像素可解码性。
5. 封面抽取：探测完成后（`ffprobe` 不可用时降级后同样执行）由 `ffmpeg` 抽取首帧（10 秒超时，`-frames:v 1` 输出 JPEG），写入 `uploads/cover/<媒体文件名>.jpg`，响应回传 `cover_url` 与 `duration_ms`；发布时前端把两者一并提交，`duration_ms` 写入 `pf_clips`，负值按 0 处理。
6. 降级策略：`ffprobe`/`ffmpeg` 缺失、执行失败或超时只记录 `warning`，响应仍为 201，仅省略 `cover_url`/`duration_ms`；`ffprobe` 不可用时跳过时长/分辨率/编码校验，只保留扩展名校验，并在日志写明跳过项。`ffprobe` 明确判定内容非法则不降级，删除已落盘文件并返回 400。
7. 前端在缺少 `cover_url` 时回退为暗色首字占位封面，`duration_ms=0` 时不渲染时长角标。
8. 已知限制：不做 faststart 强制校验，理由见 6.4。

## 3. 一致性与幂等

- 写接口按 `Idempotency-Key` 去重；MQ 事件按 `event_id` 去重。
- 互动计数只在状态变化时更新，重复点赞/收藏不累加。
- 播放计数与 QoS 聚合均以首次插入为准；QoS 采样率由前端在会话结算时抽样，只影响是否上报，不影响已落库数据；后端不按该值过滤上报。
- 字符计数统一按 Unicode 码点（Go `utf8.RuneCountInString`）：标题 128、简介 1000、昵称 64、评论 1000，去首尾空白后判定，与数据库列宽一致；前端 `maxLength` 与提示口径见 README「字符计数口径」。
- GORM `AutoMigrate` 对重复列/索引错误做 3 次退避重试；Worker 等待 API 迁移完成后再启动，避免并发建表。

## 4. 可观测性

- API 与 Worker 各自持有独立 Prometheus Registry，指标前缀 `pulsefeed_`。
- HTTP 中间件在注册路由前挂载，记录请求量、状态码与延迟。
- Feed 指标覆盖场景请求、缓存命中/未命中、分页耗时；Worker 指标覆盖消费事件数与结果；播放指标覆盖 QoS 上报、延迟直方图与卡顿。
- Grafana 通过 provisioning 自动加载 `pulsefeed-prometheus` 数据源与 16 面板仪表盘；Prometheus 抓取 `api:8090/metrics` 与 `worker:9092/metrics`。

## 5. 部署形态

- 本地：`docker compose` 启动 MySQL/Redis/RabbitMQ/API/Worker/Web，`monitoring` profile 额外启动 Prometheus/Grafana；宿主端口由 `apps/.env` 覆盖（模板 `apps/.env.example`），容器内端口与服务名固定。
- 集群：`apps/deploy.yaml` 提供 Namespace、Secret、PVC、6 个 Deployment 与对应 Service，探针使用 `/health`、`/metrics`、`/`。K8s 清单尚未做集群级校验。

## 6. 决策记录（明确不做）

### 6.1 latest 流不做预加载 / 预热

- **结论**：不实现 latest 的 preload / warmup / 定时预热，只保留"首次请求回源 + 页缓存"（`application/feed/service.go` 的 `loadFeedPage`）。TODO 保留，不落地。
- **现状**：latest 只有首次请求回源并回填缓存这一条读取路径，本项不做属于维持现状，不是新增缺口。
- **理由**：
  1. 首页页缓存 TTL 仅 5s（`infra/cache/feed_cache.go` 的 `firstPageTTL`），预热结果最多 5 秒即失效，有效期过短，预热成本换不回命中率。
  2. 预热写入的是全局首页页缓存，发布/删除/状态变更后缺少逐页失效手段；一旦预热数据过期，全站首页会看到旧内容。
  3. 预热请求的 `limit`/游标组合与真实流量不一致，命中率无法保证，反而可能挤掉真实请求的缓存。
- **替代方向**：若要降低首屏回源延迟，优先依赖已有的卡片缓存（15min）与计数缓存（15s），以及 `pf_clips` 的 timeline 索引，而不是页级预热。

### 6.2 复合游标沿用 `OR` 展开，不改行比较

- **结论**：latest/following 的游标条件保持 `published_at < ? OR (published_at = ? AND id < ?)`，不改成 `(published_at, id) < (?, ?)`。
- **依据**：在本机 MySQL 8.0 实例上用 20 万行测试数据实测（compose 镜像为 8.4，升级时需复跑 EXPLAIN），`OR` 形式被优化为 `idx_clip_timeline` 的范围扫描（`type=range`，深游标 0.3ms）；行比较形式退化为索引全扫 + `Filter`（扫 14.7 万行，535ms）。
- **取舍**：`OR` 写法略啰嗦，但能真正走索引；只有换到明确支持行比较下推的存储时再重新评估。

### 6.3 已知测试缺口（本轮不补）

- **RabbitMQ 重投递**：`ActionWorker` 的幂等已有单测覆盖（同一事件消费两次、以及“先成功后失败”分支）；死信路由已由 `test/integration/dlq_integration_test.go`（`-tags=integration`）覆盖不可重试直接入死信、连续失败 3 次入死信与原队列无残留；仍缺连接中断重连与多实例并发消费的 broker 级回归。
- **前端自动化**：`apps/web` 目前只有 CI 构建（`npm ci` + `vite build`），没有组件测试或 E2E；交互回归仍靠手工。
- **真实 MySQL 迁移**：索引建法只在本地 8.0 实例验证过，没有 testcontainers 回归，MySQL 版本升级时需要重新跑 EXPLAIN。
- **已补覆盖**：`apps/api/test/integration`（`//go:build integration`）已覆盖 `play_count` 跨表同 `event_id` 去重（含并发）、latest 游标分页边界与死信路由；每次运行使用独立 schema，跑完清理。

### 6.4 已知限制（本轮不做）

- **faststart 不做强制校验**：静态路由已支持 Range，解析 moov/mdat 顺序对 mov/webm 容易误判，收益与实现成本不成比例。
- **WebP 只做容器级校验**：Go 标准库没有 WebP 解码器，当前只校验 RIFF/WEBP 头与 VP8/VP8L/VP8X 块存在性，不解码像素。
- **`buffer_ms` 仅全局维度**：按平台/网络维度细分需要调整播放配置表主键，本轮未做。

### 6.5 词汇与命名约束

- `video_url`、`kind=video`、`<video>` 等既有对外契约字段与标签保留原名，不做重命名；词汇约束只适用于新增命名与文案，目的是避免出现项目名与可追溯措辞，不构成待办事项。后续新增字段与文案继续使用 clip / media / impression 等词汇。
