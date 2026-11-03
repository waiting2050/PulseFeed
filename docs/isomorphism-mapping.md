# 原文件 → 新文件映射表

> 约定：左侧省略公共前缀 `apps/api/internal/`；`{a,b}.go` 表示同目录多个文件逐一对应；`<原前缀>` 表示原项目的指标/文件前缀，本文不重复该前缀本身。所有新 Go 文件首行均为职责注释。

## 1. 根与运维

| 原文件 | 新文件 | 保留的设计 |
|---|---|---|
| `README.md` | `pulsefeed/README.md` | 定位、能力、启动方式、端口表、接口表 |
| `docs/architecture.md` | `pulsefeed/docs/architecture.md` | 分层边界 + 数据流；按新领域重写 |
| `docs/` 其余 8 份专题文档 | 不复刻 | 非架构骨架，本轮不静默忽略 |
| `scripts/start.sh` | `pulsefeed/scripts/start.sh` | 多进程启动 + 信号清理 |
| `apps/docker-compose.yml` | 同名 | 8 服务编排、healthcheck、depends_on、卷挂载 |
| `apps/deploy.yaml` | 同名 | Namespace/Secret/PVC/Deployment/Service/探针；实际 6 个 Deployment |
| `apps/monitoring/prometheus.yml` | 同名 | 双抓取目标 `api:8090`、`worker:9092` |
| `apps/monitoring/grafana/dashboards/<原前缀>-overview.json` | `pulsefeed-overview.json` | 四类面板结构；指标名改为 `pulsefeed_` 前缀 |
| `apps/monitoring/grafana/provisioning/dashboards/<原前缀>.yml` | `pulsefeed.yml` | dashboard provider |
| `apps/monitoring/grafana/provisioning/datasources/prometheus.yml` | 同名 | datasource provider |
| `apps/monitoring/grafana/provisioning/{alerting,plugins}/.gitkeep` | 同名 | 空占位目录 |
| `apps/api/Dockerfile` | 同名 | 多阶段构建双二进制，运行镜像装 ffmpeg |
| `apps/api/configs/{config,config.docker}.yaml` | 同名 | 配置键结构一致，仅值替换 |
| `apps/api/go.mod` | 同名 | module 化与显式依赖 |
| `apps/web/{index.html,package.json,vite.config.js,nginx.conf,Dockerfile,.gitignore}` + `src/{main,App}.jsx,styles.css` | 同名同路径 | Vite + React 单页、dev proxy、nginx 反代、多阶段镜像 |

## 2. domain

| 原文件 | 新文件 | 保留的设计 |
|---|---|---|
| `domain/account/{entity,errors,repository}.go` | 同名 | 实体 + 错误哨兵 + 仓储端口 |
| `domain/video/{entity,errors,repository}.go` | `domain/clip/{...}` | clip 词汇、发布状态、`Cursor{PublishedAt,ID}` |
| `domain/exposure/{entity,errors,repository}.go` | `domain/impression/{...}` | impression 词汇、幂等键与端口 |
| `domain/embedding/{entity,errors,hash_ngram,hash_ngram_test,repository}.go` | 同名 | FNV n-gram、256 维、余弦相似度 |
| `domain/feed/{entity,errors,repository}.go` | 同名 | Scene 枚举、Base64 游标、`ScoreHot`、批量读取端口 |
| `domain/interaction/{entity,errors,repository}.go` | 同名 | `ActionType ∈ like|save`、评论长度、端口 |
| `domain/message/{entity,errors,repository}.go` | 同名 | 消息实体、未读计数、事件 ID 幂等端口 |
| `domain/playback/{entity,errors,repository}.go` | 同名 | 播放配置/QoS 实体与端口 |
| `domain/recommendation/{entity,errors,repository}.go` | 同名 | 候选、兴趣向量、0.70/0.20/0.10 权重、`Rank` |
| `domain/relation/{entity,errors,repository}.go` | 同名 | 关注状态机、列表游标、`MaxLimit`、端口 |

## 3. application

| 原文件 | 新文件 | 保留的设计 |
|---|---|---|
| `application/account/service.go` | 同名 | 注册/登录/资料/登出，Option 注入 |
| `application/video/{service,event,fanout_worker}.go` | `application/clip/{...}` | 幂等发布、事件载荷、10000 阈值、500/批、裁剪 1000 |
| `application/exposure/{service,event}.go` | `application/impression/{service,event,worker}.go` | 上报→落库→曝光 upsert→发事件；新增 Worker（D7） |
| `application/embedding/{service,worker}.go` | 同名 | 向量生成与 MQ 消费循环 |
| `application/feed/service.go` | 同名 | Strategy 接口、`GetFeed`、`loadFeedPage`、组装 entries |
| `application/interaction/{service,worker}.go` | 同名 | 同步路径 + Redis 先写 → MQ → 失败回退；ActionWorker |
| `application/message/service.go` | 同名 | 事件驱动建消息、事件 ID 幂等、未读维护 |
| `application/playback/service.go` | 同名 | 配置下发、预加载、QoS 幂等 |
| `application/recommendation/service.go` | 同名 | 候选→打分→过滤→截断→游标；D6 修正 |
| `application/relation/service.go` | 同名 | 关注/取关、列表游标、inbox 回填、事件发布 |
| （原项目无） | `application/observability/ports.go` | 新增指标端口，应用层不 import infra |

## 4. infra

| 原文件 | 新文件 | 保留的设计 |
|---|---|---|
| `infra/cache/redis.go` | 同名 | go-redis 客户端；空 addr 禁用并降级 |
| `infra/cache/{feed_cache.go,feed_cache_test.go}` | 同名 | 页/卡片/计数三级缓存、TTL 抖动、16 分片、分钟桶、WATCH 事务；key 前缀 `pf:v1` |
| `infra/config/{entity,config}.go` | 同名 | YAML 严格加载，缺字段报错 |
| `infra/database/database.go` | 同名 | 连接池参数集中定义 |
| `infra/httpgin/gin.go` | 同名 | ReleaseMode + `Run(addr)` |
| `infra/jwt/jwt.go` | 同名 | HS256、`access_ttl` |
| `infra/metrics/metrics.go` | 同名 | Prometheus 注册，前缀 `pulsefeed_` |
| `infra/mq/rabbitmq.go` | 同名 | 三组 exchange、Publish 封装、手动 ack |
| `infra/persistence/account/{model,gorm}.go` | 同名 | `pf_users`/`pf_user_profiles` |
| `infra/persistence/video/{model,gorm}.go` | `infra/persistence/clip/{...}` | `pf_clips`/`pf_clip_stats`、事务、幂等键 |
| `infra/persistence/exposure/{model,gorm}.go` | `infra/persistence/impression/{...}` | `pf_play_events`/`pf_impressions`、唯一键与 upsert |
| `infra/persistence/embedding/{model,gorm}.go` | 同名 | 向量批量读写 |
| `infra/persistence/feed/{model,gorm}.go` | 同名 | 复合游标 SQL、`limit+1`、批量读取 |
| `infra/persistence/interaction/{model,gorm}.go` | 同名 | 唯一键、`FOR UPDATE`、仅状态变化改计数 |
| `infra/persistence/message/{model,gorm}.go` | 同名 | `uk_event_id`、列表/已读/未读 |
| `infra/persistence/playback/{model,gorm}.go` | 同名 | 配置与 QoS 表 |
| `infra/persistence/recommendation/gorm.go` | 同名 | 刻意保留“仅 gorm.go、无 model.go”的不对称 |
| `infra/persistence/relation/{model,gorm}.go` | 同名 | `pf_follows` 唯一键、`pf_user_stats` 双计数事务 |
| `infra/persistence/migration/migration.go` | 同名 | AutoMigrate + 重复列/索引 3 次退避 |

## 5. interfaces / cmd / test

| 原文件 | 新文件 | 保留的设计 |
|---|---|---|
| `interfaces/http/router/router.go` | 同名 | 组合根装配、`/api` 与 `/internal` 分组、条件启用 Redis/MQ、`/health` `/metrics` `/uploads` |
| `interfaces/http/middleware/jwt_auth.go` | 同名 | 必选/可选 JWT、内部 token 常量时间比较 |
| `interfaces/http/account/{dto,handler}.go` | 同名 | DTO 校验与错误映射 |
| `interfaces/http/video/{dto,handler}.go` | `interfaces/http/clip/{...}` | 发布/详情/删除/列表 |
| `interfaces/http/exposure/{dto,handler}.go` | `interfaces/http/impression/{...}` | 播放事件上报 |
| `interfaces/http/{feed,interaction,message,playback,relation}/{dto,handler}.go` | 同名 | 各端点形状与信封 |
| `interfaces/http/recommendation/{dto,handler}.go` | 同名 | 内部候选/曝光决策 + 内部 token |
| `interfaces/http/upload/{handler,handler_test}.go` | 同名 | kind 落盘、扩展名白名单、ffprobe 降级 |
| `cmd/feed/main.go` | `cmd/api/main.go` | HTTP 组合根 |
| `cmd/worker/main.go` | 同名 | MQ 消费 + 独立 metrics + 信号退出 |
| `test/account_api_test.go` | `test/account_test.go` + `internal/application/account/account_test.go` | httptest 流程风格 + 服务单元断言 |
| `test/video_api_test.go` | `test/clip_test.go` + `internal/application/clip/clip_test.go` | 发布/详情/幂等 |
| `test/exposure_api_test.go` | `internal/application/impression/impression_test.go` | 上报幂等与曝光 |
| `test/feed_api_test.go` | `test/feed_test.go` + `internal/application/feed/feed_test.go` | 分页 `has_more` 与跨页不重复 |
| `test/interaction_api_test.go` | `internal/application/interaction/interaction_test.go` | 点赞/收藏/评论幂等与计数 |
| `test/message_api_test.go` | `internal/application/message/message_test.go` | 事件幂等、未读与已读 |
| `test/playback_api_test.go` | `internal/application/playback/playback_test.go` | 配置、预加载、QoS 幂等 |
| `test/relation_api_test.go` | `internal/application/relation/relation_test.go` | 关注/取关幂等与分页 |
| `test/upload_api_test.go` | `internal/interfaces/http/upload/upload_test.go` | 扩展名/kind/落盘 |
| `test/upload_static_range_test.go` | `internal/interfaces/http/router/upload_static_range_test.go` | Range 206 与 Content-Range |
| `test/recommendation_api_test.go` | `internal/application/recommendation/recommendation_test.go` | 排序与跨分钟游标稳定 |
| `test/video_embedding_worker_test.go` | `internal/application/embedding/clip_embedding_worker_test.go` | 消费→向量落库 |
| `test/video_fanout_worker_test.go` | `internal/application/clip/clip_fanout_worker_test.go` | 扇出阈值与分批 |

## 6. 一对一之外的差异

| 类型 | 内容 |
|---|---|
| 新增 | `application/observability/ports.go`（依赖倒置）；`application/impression/worker.go`（D7）；`docs/isomorphism-mapping.md` |
| 更名 | `cmd/feed` → `cmd/api`；模块前缀统一为 `pulsefeed` / `pf` |
| 有意修正 D6 | 推荐游标携带 `snapshot_at`，`next_cursor` 在按创作者交错后生成 |
| 有意修正 D7 | 新增 `PlayEventWorker`，消费 `impression.play_recorded` |
| 未核实处采用方案 C5 | 页缓存 key = `sha1(scene|viewer|cursor|limit)[:16]`；hot 窗口 = 最近 60 个分钟桶；推荐候选池 50..500；Grafana 最小 provisioning |
| 不复刻 | `template/`、`openspec/`；原 `docs/` 的 8 份专题文档 |
| 计数偏差修正 | 计划原文写“5 个 Deployment”按实际修正为 6 个：mysql/redis/rabbitmq/api/worker/web |
