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
| 前端 | React 18 + Vite 5，开发态代理 `/api`、`/uploads` 到 8090 |
| 编排 | Docker Compose；Prometheus + Grafana 位于 `monitoring` profile |

## 目录结构

```
pulsefeed/
├── README.md
├── docs/
│   ├── architecture.md            # 分层边界与四条数据流
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
    │   └── test/                  # 基于内存 fake 的接口测试
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

健康检查与指标：`GET :8090/health`、`GET :8090/metrics`、Worker `GET :19092/metrics`。

## 容器化与监控

```bash
cd pulsefeed/apps
docker compose up -d --build
docker compose --profile monitoring up -d prometheus grafana
docker compose --profile monitoring down   # 只停止并移除容器，保留数据卷
```

| 服务 | 宿主机端口 | 容器端口 | 说明 |
|---|---|---|---|
| web | 8088 | 80 | nginx 托管构建产物，反代 `/api/`、`/uploads/` |
| api | 8090 | 8090 | HTTP API 与 `/health`、`/metrics` |
| worker | 19092 | 9092 | 指标端口固定映射，避开 Win11 Hyper-V 保留段 9020–9119 |
| mysql | 3307 | 3306 | |
| redis | 6380 | 6379 | |
| rabbitmq | 5673 / 15673 | 5672 / 15672 | 15673 为管理台 |
| prometheus | 19090 | 9090 | 仅 `monitoring` profile；9090 同样落在保留段，故映射 19090 |
| grafana | 3000 | 3000 | 仅 `monitoring` profile，预置数据源与 16 面板仪表盘 |

监控验收口径为 `http://localhost:19090`（Prometheus）与 `http://localhost:3000`（Grafana，默认账号 `admin` / `pulsefeed`）。K8s 清单 `deploy.yaml` 含 6 个 Deployment（mysql/redis/rabbitmq/api/worker/web）。

## 阶段范围

- 已完成：S1–S17 后端主链路（账号、发布、Feed 四场景、互动、关注、推荐、热榜、消息、播放配置、可观测性）。
- 已完成：S18/S19 前端最小闭环与功能补齐（登录/注册、Feed 分页、发布、互动、消息、播放配置）。
- 已完成：S20 容器化与本地编排；S21 Prometheus/Grafana 监控与 K8s 清单。
- 已完成：S22 测试补齐（鉴权中间件矩阵、Redis WATCH/16 分片计数、互动幂等）与 GitHub Actions CI（api build/vet/test + web 构建）。
- 已完成：前端 P0/P1/P2 重构（视觉骨架、页面与交互重做、动效与可访问性）；媒体时长与首帧封面补齐。

### 媒体时长与首帧封面

- 上传视频时调用 `ffprobe` 读取时长（写入 `pf_clips.duration_ms`，毫秒）、调用 `ffmpeg` 抽取首帧写入 `uploads/cover/` 并作为 `cover_url` 回传；前端卡片按 `mm:ss` 展示时长角标，`duration_ms=0` 时不占位。
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
| 上传 | `POST /api/uploads`（`kind=video,cover,avatar`；视频额外回传 `cover_url`/`duration_ms`） | 登录 |
| 互动 | `PUT/DELETE /api/clips/:clipId/like`、`PUT/DELETE /api/clips/:clipId/save`、`POST/GET /api/clips/:clipId/comments`、`DELETE /api/comments/:commentId` | 登录 / 公开 |
| 播放 | `POST /api/clip-play-events`、`POST /api/playback-qos-reports`、`GET/PUT /api/playback-config`、`GET /api/preload-clips` | 登录 |
| 关注 | `PUT/DELETE /api/users/me/following/:targetUserId`、`GET /api/users/me/following`、`GET /api/users/me/followers` | 登录 |
| 消息 | `GET /api/messages`、`GET /api/message-stats/unread`、`PATCH /api/messages` | 登录 |
| 内部 | `POST /internal/recommendation-candidates`、`GET /internal/impression-decisions`、`GET /internal/impressions`、`POST /internal/messages`、`POST /internal/playback-qos-reports` | `X-Internal-Token` |

前端写请求统一携带 `Idempotency-Key`，后端按 key 幂等。

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

## 文档

- [架构与数据流](docs/architecture.md)
- [路由与端口快照](docs/routes-snapshot.md)
