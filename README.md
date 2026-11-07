# PulseFeed

PulseFeed 是一个按领域分层实现的 Feed 系统示例项目：后端提供账号、发布、Feed、互动、关注、推荐、热榜、消息与播放配置，前端提供登录、四场景 Feed、发布、互动、消息与播放配置页面。

## 技术栈

| 层 | 选型 |
|---|---|
| 语言 | Go 1.22 |
| Web 框架 | Gin（ReleaseMode） |
| 持久化 | MySQL 8.4 + GORM AutoMigrate |
| 缓存 | Redis 7.4（页/卡片/计数缓存、热度分钟桶、互动状态） |
| 消息队列 | RabbitMQ 3.13（发布扇出、互动落库、向量生成、播放事件） |
| 鉴权 | JWT HS256（`Authorization: Bearer`），内部接口使用 `X-Internal-Token` |
| 配置 | goccy/go-yaml，配置文件缺失即报错，不做默认值兜底 |
| 指标 | Prometheus client，API 与 Worker 独立 Registry，前缀 `pulsefeed_` |
| 前端 | React 18 + Vite 5 + react-router-dom 6，开发态代理 `/api`、`/uploads` 到 8090 |
| 编排 | Docker Compose；Prometheus + Grafana 位于 `monitoring` profile |

## 目录结构

```
pulsefeed/
├── README.md
├── docs/
│   ├── architecture.md            # 分层边界与关键数据流
│   ├── frontend-redesign.md       # 前端视觉与交互重构记录
│   └── routes-snapshot.md         # HTTP 路由与端口快照
├── scripts/start.sh               # 本地三进程启动与信号清理
└── apps/
    ├── api/                       # Go module（module: pulsefeed）
    │   ├── cmd/
    │   │   ├── api/               # HTTP 组合根
    │   │   └── worker/            # MQ 消费者组合根
    │   ├── configs/               # config.yaml / config.docker.yaml
    │   ├── internal/
    │   │   ├── domain/            # 实体、错误哨兵、仓储端口
    │   │   ├── application/       # 用例编排与 Worker
    │   │   ├── infra/             # MySQL/Redis/RabbitMQ/JWT/指标实现
    │   │   └── interfaces/http/   # handler、dto、路由、中间件
    │   └── test/                  # 基于内存 fake 的接口测试；test/integration 为 build tag 集成测试
    ├── web/                       # React + Vite 前端
    ├── monitoring/                # Prometheus 配置与 Grafana provisioning
    ├── docker-compose.yml
    └── deploy.yaml                # K8s 清单（Namespace/Secret/PVC/Deployment/Service）
```

分层依赖方向：`interfaces → application → domain`，`infra` 实现 `domain` 端口并在组合根注入；`application` 不直接 import `infra`。

## 两个组合根

- `apps/api/cmd/api`：加载并校验配置 → 初始化 MySQL/Redis/RabbitMQ/指标 → 装配仓储、服务、Handler 与路由 → 监听 HTTP（默认 `:8090`），提供 `/health`、`/metrics`、`/uploads` 与业务路由。
- `apps/api/cmd/worker`：加载配置 → 执行迁移 → 连接 MQ → 启动 `ActionWorker`、`FanoutWorker`、`EmbeddingWorker`、`PlayEventWorker` → 在独立指标端口提供 `/metrics`（`-metrics-addr` 默认 `:9092`，可覆盖）→ 收到信号后优雅退出。

## 本地启动

前置依赖：MySQL（3306）、Redis（6379）、RabbitMQ（5672）。连接参数位于 `apps/api/configs/config.yaml`，首次启动自动迁移；配置项包含 `port`、`jwt{secret,access_ttl}`、`internal{token}`、`database`、`redis`、`rabbitmq{url,三组 exchange/queue/routing}`。

```bash
# 后端 API
cd pulsefeed/apps/api
go run ./cmd/api

# 异步 Worker（本机 9092 可能被 Hyper-V 保留段占用，可显式覆盖）
cd pulsefeed/apps/api
go run ./cmd/worker -metrics-addr :19092

# 前端
cd pulsefeed/apps/web
npm install
npm run dev            # http://127.0.0.1:5173
```

前端开发态将 `/api`、`/uploads` 代理到 `http://127.0.0.1:8090`，目标地址可用环境变量 `PULSEFEED_API` 覆盖。

健康检查与指标：`GET :8090/health`、`GET :8090/metrics`；Worker 按上面的 `-metrics-addr` 启动时为 `GET :19092/metrics`（缺省 `:9092`）。

## 容器化与监控

```bash
cd pulsefeed/apps
docker compose up -d --build
docker compose --profile monitoring up -d prometheus grafana
docker compose --profile monitoring down   # 只停止并移除容器，保留数据卷
```

宿主端口全部可配置：下表为「默认宿主端口 + 覆盖变量 + 容器端口」，模板见 `apps/.env.example`（复制为 `apps/.env` 后生效，`.env` 不提交）。

| 服务 | 默认宿主端口 | 覆盖变量 | 容器端口 | 说明 |
|---|---|---|---|---|
| web | 8088 | `WEB_PORT` | 80 | nginx 托管构建产物，反代 `/api/`、`/uploads/` |
| api | 8090 | `API_PORT` | 8090 | HTTP API 与 `/health`、`/metrics` |
| worker | 19092 | `WORKER_METRICS_PORT` | 9092 | 指标端口，默认值避开 Win11 Hyper-V 保留段 9020–9119 |
| mysql | 3307 | `MYSQL_PORT` | 3306 | |
| redis | 6380 | `REDIS_PORT` | 6379 | |
| rabbitmq | 5673 / 15673 | `RABBITMQ_PORT` / `RABBITMQ_UI_PORT` | 5672 / 15672 | 15673 为管理台 |
| prometheus | 19090 | `PROMETHEUS_PORT` | 9090 | 仅 `monitoring` profile；9090 同样落在保留段，故默认映射 19090 |
| grafana | 3000 | `GRAFANA_PORT` | 3000 | 仅 `monitoring` profile，预置数据源与 16 面板仪表盘 |

### 端口被占用怎么办

宿主端口全部可由 `.env` 覆盖，不用改 `docker-compose.yml`：

```bash
cd pulsefeed/apps
cp .env.example .env        # Windows PowerShell: Copy-Item .env.example .env
# 编辑 .env，例如 REDIS_PORT=6381、MYSQL_PORT=3308
docker compose config --quiet   # 先校验变量替换后的配置
docker compose up -d --build
```

容器内端口与服务名固定不变（`mysql:3306`、`redis:6379`、`rabbitmq:5672`），因此改宿主映射不影响服务间通信。Windows 上 9020–9119 可能被 Hyper-V/WinNAT 动态保留，默认值 19090/19092 已避开；仍冲突时改 `PROMETHEUS_PORT` / `WORKER_METRICS_PORT` 即可。可用 `netsh interface ipv4 show excludedportrange protocol=tcp` 查看当前保留段。

监控验收口径为 `http://localhost:19090`（Prometheus）与 `http://localhost:3000`（Grafana，默认账号 `admin` / `pulsefeed`）。K8s 清单 `deploy.yaml` 含 6 个 Deployment（mysql/redis/rabbitmq/api/worker/web）。

## 当前进度

以下清单逐条对照代码与本地运行结果核实；未实现或未验证的能力不勾选。

- [x] 后端主链路：账号、Clip 发布（`Idempotency-Key` 幂等）、Feed 四场景（latest / following / for-you / hot）、互动（点赞 / 收藏 / 评论）、关注、推荐、热榜、消息、播放配置与 Prometheus 指标。
- [x] 互动异步落库：点赞/收藏先写 Redis，再投递 `pulsefeed.interaction.action_changed`，由 Worker 的 `ActionWorker` 落库；Redis 或 MQ 不可用时回退同步落库。真实 compose 运行中该队列有 1 个消费者（`clip.published`、`clip.embedding`、`impression.play_recorded` 同样各 1 个）。
- [x] 播放事件幂等：`event_id` 唯一键拦住 `pf_play_events` 重复写入；与 `pf_playback_qos_reports` 同 `event_id` 时不再累加 `play_count`。集成测试覆盖并发去重（见下方「集成测试」）。
- [x] 前端：React 18 + Vite 5，5 个页面（登录 / Feed / 发布 / 消息 / 播放配置）走独立路由；Feed 场景由 `/feed?scene=latest|hot|following|for-you` 直达，详情为 `/clip/:id?from=...` 深链弹层，刷新、前进后退与直接粘贴 URL 均正常，关闭后回到来源列表；Feed 分页仍用「加载更多」按钮，不是无限滚动。卡片与详情内联播放器支持播放 / 暂停 / 静音 / 进度 / 全屏自控，并按首帧延迟、卡顿次数、播放时长、完播状态上报 QoS（`event_id` 幂等）。依赖仅新增 `react-router-dom` 6。
- [x] 容器化与监控：Compose 编排、宿主端口可由 `.env` 覆盖；Prometheus/Grafana 位于 `monitoring` profile；K8s 清单已提供但未做集群级实测。
- [x] 测试与 CI：`go build/vet/test`、前端 `npm run build`、GitHub Actions；真实依赖集成测试通过 `-tags=integration` 手动运行。
- [x] 媒体探测：上传媒体文件时由 `ffprobe` / `ffmpeg` 读取时长与首帧，工具缺失或失败时降级，不影响发布。
- [x] RabbitMQ 重投递与死信：四组消费队列均声明 `x-dead-letter-exchange`，对应死信交换机 `pulsefeed.<域>.dlx` 与死信队列 `<原队列>.dead`；消费失败先 `basic.nack(requeue=true)` 回原队列，同一消费进程累计失败 3 次（含首次）后 nack 不重入原队列，由 broker 转进死信；不可重试错误（消息体非法、必需字段缺失、目标 clip 不可见等）直接进死信，不消耗重试次数。旧队列参数不一致时 broker 返回 406 PreconditionFailed，Worker 中止启动并提示删除旧队列。
- [ ] 前端自动化：目前只有构建校验，没有组件测试或 E2E，交互回归仍靠手工。

### 字符计数口径

- 后端按字符（Go `utf8.RuneCountInString`，Unicode 码点）计数：标题上限 128、昵称上限 64、评论上限 1000，均先 `TrimSpace` 再判定；与 `pf_clips.title`、`pf_user_profiles.nickname`、`pf_comments.content` 的列宽一致，不额外放宽。
- 前端发布页按码点展示 `X/120`，沿用既有 120 字提示上限；HTML `maxLength` 取 128（浏览器按 UTF-16 单元计数，不会少于码点数），因此不会出现前端放行、后端拒绝。昵称输入同步限制 64。

### 媒体时长与首帧封面

- 上传媒体文件时调用 `ffprobe` 读取时长（写入 `pf_clips.duration_ms`，毫秒）、调用 `ffmpeg` 抽取首帧写入 `uploads/cover/` 并作为 `cover_url` 回传；前端卡片按 `mm:ss` 展示时长角标，`duration_ms=0` 时不占位。
- 工具缺失或执行失败时只记录 `warning` 并降级为“无时长、无封面”，上传与发布照常成功；前端回退为暗色首字占位封面。
- 数据库沿用 GORM `AutoMigrate` 增量新增 `duration_ms` 列；API 镜像（`apps/api/Dockerfile`）已内置 `ffmpeg`，本地 Windows 不安装也能运行。

## 接口表

统一响应信封 `{code, message, data}`，成功 `code=0`。完整请求响应字段见 `docs/routes-snapshot.md`。

| 分组 | 方法与路径 | 鉴权 |
|---|---|---|
| 账号 | `POST /api/users`、`POST /api/sessions`、`DELETE /api/sessions/current` | 公开 / 登录 |
| 资料 | `GET/PATCH /api/users/me`、`GET /api/users/:userId`、`GET /api/users/:userId/clips` | 登录 / 公开 |
| Feed | `GET /api/feed`、`POST /api/feed-search`（`scene=latest,following,for-you,hot`） | 可选登录 |
| 发布 | `POST /api/clips`（可选 `duration_ms`）、`DELETE /api/clips/:clipId`、`GET /api/clips/:clipId`、`GET /api/users/me/clips` | 登录 / 公开 |
| 上传 | `POST /api/uploads`（`kind=video,cover,avatar`；媒体文件额外回传 `cover_url`/`duration_ms`） | 登录 |
| 互动 | `PUT/DELETE /api/clips/:clipId/like`、`PUT/DELETE /api/clips/:clipId/save`、`POST/GET /api/clips/:clipId/comments`、`DELETE /api/comments/:commentId` | 登录 / 公开 |
| 播放 | `POST /api/clip-play-events`、`POST /api/playback-qos-reports`、`GET/PUT /api/playback-config`、`GET /api/preload-clips` | 登录 |
| 关注 | `PUT/DELETE /api/users/me/following/:targetUserId`、`GET /api/users/me/following`、`GET /api/users/me/followers` | 登录 |
| 消息 | `GET /api/messages`、`GET /api/message-stats/unread`、`PATCH /api/messages` | 登录 |
| 内部 | `POST /internal/recommendation-candidates`、`GET /internal/impression-decisions`、`GET /internal/impressions`、`POST /internal/messages`、`POST /internal/playback-qos-reports` | `X-Internal-Token` |

前端对发布与评论写请求携带 `Idempotency-Key`：发布要求非空且同 key 返回同一 clip，评论同 key 重放返回同一评论；互动、关注、评论对去空白后超 128 字符的 key 返回 400，点赞/收藏/关注由唯一行天然幂等。

## 测试与校验

```bash
cd pulsefeed/apps/api
gofmt -l .            # 工作区为 CRLF 时需按 LF 归一化复核
go build ./...
go vet ./...
go test ./...

cd ../web
npm run build
```

禁用字样自查：对仓库执行关键词检索，输出为空即通过；具体关键词不在文档中重复。

### 集成测试（需真实 MySQL / Redis / RabbitMQ）

用 build tag 隔离，默认 `go test ./...` 不编译该目录。测试每次运行会创建独立 schema（`pulsefeed_it_<timestamp>`），跑完自动 `DROP`，不会读写演示数据。

```bash
cd pulsefeed/apps
docker compose up -d mysql redis rabbitmq   # 端口被占用时先按上文用 .env 换端口

cd api
# PowerShell 用 $env:VAR='...' 逐条设置；默认端口对应 compose 默认值
PULSEFEED_IT_MYSQL_DSN='root:pulsefeed-root@tcp(127.0.0.1:3307)/?charset=utf8mb4&parseTime=True&loc=Local' \
PULSEFEED_IT_REDIS_ADDR='127.0.0.1:6380' \
PULSEFEED_IT_RABBITMQ_URL='amqp://pulsefeed:pulsefeed@127.0.0.1:5673/' \
go test -tags=integration ./test/integration -v -count=1
```

| 变量 | 默认值 | 说明 |
|---|---|---|
| `PULSEFEED_IT_MYSQL_DSN` | `root:pulsefeed-root@tcp(127.0.0.1:3307)/...` | 需 root 权限创建/删除独立 schema |
| `PULSEFEED_IT_REDIS_ADDR` | `127.0.0.1:6380` | 仅连通性检查 |
| `PULSEFEED_IT_RABBITMQ_URL` | `amqp://pulsefeed:pulsefeed@127.0.0.1:5673/` | 仅连通性检查 |

排障：先 `docker compose ps` 确认三个依赖为 healthy；`CREATE DATABASE` 报权限不足时检查 DSN 是否用了 root；连接超时优先核对 `.env` 实际映射的宿主端口与上述变量是否一致。测试覆盖：`play_count` 跨表同 `event_id` 去重（含两个 goroutine 并发上报）、latest 场景逐页游标边界（含跨分钟）与死信路由（不可重试直接入死信、连续失败 3 次后入死信、原队列无残留）。

## 文档

- [架构与数据流](docs/architecture.md)
- [路由与端口快照](docs/routes-snapshot.md)
- [前端重构与实施记录](docs/frontend-redesign.md)
