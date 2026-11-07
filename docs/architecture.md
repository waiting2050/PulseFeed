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

1. `POST /api/clips` 校验登录、标题与地址，按 `Idempotency-Key` 幂等写入 `pf_clips` 与 `pf_clip_stats`。
2. 发布成功后投递 `clip.published`（含 `clip_id`、`creator_id`、`published_at`）。
3. `FanoutWorker` 读取创作者粉丝数：达到大 V 阈值只写创作者 outbox（拉模型）；否则按 500/批写入粉丝 inbox（推模型），并预热卡片与计数缓存。
4. 关注类 Feed 优先读 inbox/outbox，未命中时回源 DB 并按游标分页。

### 2.2 互动

1. 点赞/收藏先以 `(user_id, clip_id, action_type)` 幂等更新 Redis 与 DB 状态，计数只在状态变化时增减。
2. 状态变化后投递 `interaction.action_changed`；`ActionWorker` 消费后维护计数与消息事件。
3. 评论走同步落库，按 `Idempotency-Key` 去重；评论数随创建/删除同步更新。
4. 前端对点赞/收藏做乐观更新，请求失败时回滚。

### 2.3 展示（impression）、播放与 QoS

1. `POST /api/clip-play-events` 按 `event_id` 幂等写入 `pf_play_events`，并 upsert 当日展示聚合；同一 `event_id` 不重复累加 `play_count`，与 `pf_playback_qos_reports` 相同的 `event_id` 也只计一次。
2. 事件投递 `impression.play_recorded`；`PlayEventWorker`（D7 修正）消费后完成展示聚合补偿，避免“只发不收”。
3. `POST /api/playback-qos-reports` 按 `event_id` 幂等写入 QoS 表；仅首次插入参与采样与延迟/卡顿/完播聚合。
4. 播放配置按用户持久化 `preload_count`、`qos_sample_rate`；预加载接口返回最新 N 个可见 clip。

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

1. `POST /api/uploads` 先按 `kind` 校验扩展名白名单并落盘；仅 `kind=video` 额外用 `ffprobe` 校验内容可读性（5s 超时）。`cover` / `avatar` 只做扩展名校验。
2. 校验通过后由 `infra/media.Prober` 读取时长（`ffprobe`，5s 超时，四舍五入为毫秒）并调用 `ffmpeg` 抽取首帧（10s 超时，`-frames:v 1` 输出 JPEG）。
3. 首帧写入 `uploads/cover/<媒体文件名>.jpg`，响应回传 `cover_url` 与 `duration_ms`；发布时前端把两者一并提交，`duration_ms` 写入 `pf_clips`，负值按 0 处理。
4. 降级策略：`ffprobe`/`ffmpeg` 缺失、执行失败或超时只记录 `warning`，响应仍为 201，仅省略 `cover_url`/`duration_ms`；`ffprobe` 明确判定内容非法则不降级，删除已落盘文件并返回 400。
5. 前端在缺少 `cover_url` 时回退为暗色首字占位封面，`duration_ms=0` 时不渲染时长角标。

## 3. 一致性与幂等

- 写接口按 `Idempotency-Key` 去重；MQ 事件按 `event_id` 去重。
- 互动计数只在状态变化时更新，重复点赞/收藏不累加。
- 播放计数与 QoS 聚合均以首次插入为准；QoS 采样率只影响上报与否，不影响已落库数据。
- 字符计数统一按 Unicode 码点（Go `utf8.RuneCountInString`）：标题 128、昵称 64、评论 1000，去首尾空白后判定，与数据库列宽一致；前端 `maxLength` 与提示口径见 README「字符计数口径」。
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

- **RabbitMQ 重投递**：`ActionWorker` 的幂等已有单测覆盖（同一事件消费两次、以及“先成功后失败”分支），但 broker 层的 basic.nack/requeue 与死信未做集成验证，需要真实 MQ 或 testcontainers。
- **前端自动化**：`apps/web` 目前只有 CI 构建（`npm ci` + `vite build`），没有组件测试或 E2E；交互回归仍靠手工。
- **真实 MySQL 迁移**：索引建法只在本地 8.0 实例验证过，没有 testcontainers 回归，MySQL 版本升级时需要重新跑 EXPLAIN。
- **已补覆盖**：`apps/api/test/integration`（`//go:build integration`）已覆盖 `play_count` 跨表同 `event_id` 去重（含并发）与 latest 游标分页边界；每次运行使用独立 schema，跑完清理。
